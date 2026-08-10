package nosqlite

// scan.go executes a query.
//
// v1 has no secondary indexes, so every query is a full scan of the collection.
// The important property is that it is a *streaming* full scan: peak memory is
// a function of the result size, not of the collection size.
//
//	snapshot the index under the read lock, release it
//	for each of this collection's records, in offset order:
//	    read the payload into a reused scratch buffer
//	    json.Unmarshal into a reused scratch map     <- only per-document alloc
//	    Matcher.Match
//	    no match -> clear the scratch map and move on, nothing retained
//	    match    -> deep-copy the document into the result
//	apply skip / limit
//
// A non-matching document costs one parse and leaves nothing behind.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jkristia/nosqlite/internal/engine"
)

// scanStats are the numbers the trace file reports for every query. With no
// indexes, "why is this slow" is always answered by the ratio between them —
// this is the closest thing to EXPLAIN QUERY PLAN the database has.
type scanStats struct {
	scanned  int // documents read and parsed
	matched  int // documents the filter accepted
	returned int // documents actually handed to the caller after skip/limit
}

// sequentialScanRatio decides between the two read shapes below. When this
// collection owns at least this fraction of the file's records, one buffered
// pass over the whole file beats jumping between its own records.
const sequentialScanRatio = 0.4

// scanRecords walks every document in the snapshot in insertion order, calling
// visit with the raw payload bytes.
//
// The payload slice is only valid for the duration of the call: it points into
// a buffer that gets reused for the next record. visit returns (keepGoing,
// error).
func (c *Collection) scanRecords(snap snapshot, visit func(i int, payload []byte) (bool, error)) error {
	n := snap.n()
	if n == 0 {
		return nil
	}
	// Both paths return exactly the same documents in the same order; the
	// choice is purely about how many bytes get read.
	if snap.total > 0 && float64(n)/float64(snap.total) >= sequentialScanRatio {
		return c.scanSequential(snap, visit)
	}
	return c.scanStrided(snap, visit)
}

// scanSequential makes one buffered pass over the file, skipping records that
// belong to other collections.
//
// This is the common case: most databases have one collection that dominates
// the file, and reading it back-to-back is what disks and page caches are
// happiest doing.
func (c *Collection) scanSequential(snap snapshot, visit func(int, []byte) (bool, error)) error {
	// Reading through a SectionReader bounded by the snapshot's file size means
	// a concurrent writer appending right now is invisible to this scan — which
	// is exactly the snapshot semantics we promise.
	section := io.NewSectionReader(c.db.file, headerSize, snap.size-headerSize)
	r := bufio.NewReaderSize(section, 64<<10)

	var hdr [recordHeaderSize]byte
	var buf []byte
	n := snap.n()

	for i := 0; i < n; {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // reached the snapshot boundary
			}
			return fmt.Errorf("nosqlite: scanning %s: %w", c.name, err)
		}
		length := binary.LittleEndian.Uint32(hdr[0:4])
		op := hdr[4]
		coll := binary.LittleEndian.Uint16(hdr[6:8])

		if coll != c.id || op != opInsert {
			// Not ours. Discard skips the bytes without copying them out.
			if _, err := r.Discard(int(length)); err != nil {
				return fmt.Errorf("nosqlite: scanning %s: %w", c.name, err)
			}
			continue
		}

		if cap(buf) < int(length) {
			buf = make([]byte, length)
		}
		buf = buf[:length]
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("nosqlite: scanning %s: %w", c.name, err)
		}

		keepGoing, err := visit(i, buf)
		i++
		if err != nil || !keepGoing {
			return err
		}
	}
	return nil
}

// scanStrided reads only this collection's records, one ReadAt each.
//
// Used when the collection is a small tenant of a large file, where a
// sequential pass would spend most of its time reading other collections. The
// offsets ascend, so this is a forward-only strided read that the OS page cache
// handles well.
//
// ReadAt is pread(2) underneath: it does not touch the file's shared seek
// offset, so it is safe to use concurrently with a writer appending to the very
// same *os.File. That is what lets readers run with no lock at all.
func (c *Collection) scanStrided(snap snapshot, visit func(int, []byte) (bool, error)) error {
	var buf []byte
	for i := 0; i < snap.n(); i++ {
		length := int(snap.lengths[i])
		if cap(buf) < length {
			buf = make([]byte, length)
		}
		buf = buf[:length]
		if _, err := c.db.file.ReadAt(buf, snap.offsets[i]); err != nil {
			return fmt.Errorf("nosqlite: reading %s document at offset %d: %w",
				c.name, snap.offsets[i], err)
		}
		keepGoing, err := visit(i, buf)
		if err != nil || !keepGoing {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Query execution
// ---------------------------------------------------------------------------

// runQuery executes q and calls emit once per surviving document, in result
// order. emit owns the document it is handed — it is a fresh deep copy.
//
// This one function backs Find, FindOne, ForEach and Count.
func (c *Collection) runQuery(q Query, emit func(doc map[string]any) error) (scanStats, error) {
	var stats scanStats

	if err := q.validate(); err != nil {
		return stats, err
	}
	matcher, err := CompileFilter(q.Filter)
	if err != nil {
		return stats, err
	}

	started := time.Now()
	snap := c.snapshot()

	if len(q.Sort) == 0 {
		err = c.runUnsorted(snap, matcher, q, &stats, emit)
	} else {
		err = c.runSorted(snap, matcher, q, &stats, emit)
	}

	c.db.traceFind(c.name, q, stats, time.Since(started), err)
	return stats, err
}

// runUnsorted is the cheap shape: results come out in insertion order, so the
// scan can stop the moment Skip+Limit documents have matched. Memory is
// O(Limit), and so is the work when the limit is small and matches are common.
func (c *Collection) runUnsorted(snap snapshot, m Matcher, q Query, stats *scanStats,
	emit func(map[string]any) error) error {

	scratch := make(map[string]any)

	return c.scanRecords(snap, func(_ int, payload []byte) (bool, error) {
		stats.scanned++

		// clear() empties the map but keeps its allocated buckets, so the next
		// document reuses them. json.Unmarshal into an existing map ADDS keys
		// rather than replacing the map, so this clear is required for
		// correctness, not just for speed.
		clear(scratch)
		if err := json.Unmarshal(payload, &scratch); err != nil {
			return false, fmt.Errorf("nosqlite: decoding document in %s: %w", c.name, err)
		}
		if !m.Match(scratch) {
			return true, nil // dropped; nothing retained
		}
		stats.matched++
		if stats.matched <= q.Skip {
			return true, nil
		}
		stats.returned++
		// Deep copy: the scratch map is about to be reused, and a caller must
		// not be able to corrupt the engine by mutating a result.
		if err := emit(deepCopyMap(scratch)); err != nil {
			return false, err
		}
		if q.Limit > 0 && stats.returned >= q.Limit {
			return false, nil // done: stop scanning
		}
		return true, nil
	})
}

// runSorted handles the two sorted shapes.
//
//   - Sort WITH a limit: a bounded heap of Skip+Limit entries (engine.TopK).
//     Memory is O(Skip+Limit) regardless of how many documents match.
//   - Sort WITHOUT a limit: every match has to be materialised before it can be
//     ordered. This is the one query shape that can still exhaust memory. It is
//     inherent to the operation rather than to this design — adding a Limit is
//     the answer.
func (c *Collection) runSorted(snap snapshot, m Matcher, q Query, stats *scanStats,
	emit func(map[string]any) error) error {

	paths := q.sortPaths()
	scratch := make(map[string]any)

	bounded := q.Limit > 0
	k := q.Skip + q.Limit

	var heapK *engine.TopK
	var all []engine.SortEntry
	if bounded {
		heapK = engine.NewTopK(k, q.Sort)
	}

	err := c.scanRecords(snap, func(_ int, payload []byte) (bool, error) {
		stats.scanned++
		clear(scratch)
		if err := json.Unmarshal(payload, &scratch); err != nil {
			return false, fmt.Errorf("nosqlite: decoding document in %s: %w", c.name, err)
		}
		if !m.Match(scratch) {
			return true, nil
		}
		stats.matched++

		// Extract sort keys from the scratch map before copying, so the keys
		// are plain values and the comparison never walks paths again.
		keys := make([]any, len(paths))
		for i, p := range paths {
			keys[i] = deepCopy(engine.LookupOne(scratch, p))
		}
		entry := engine.SortEntry{Doc: deepCopyMap(scratch), Keys: keys, Seq: stats.matched}

		if bounded {
			heapK.Add(entry)
		} else {
			all = append(all, entry)
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	var ordered []engine.SortEntry
	if bounded {
		ordered = heapK.Sorted()
	} else {
		ordered = all
		engine.SortEntries(ordered, q.Sort)
	}

	// Apply skip, then hand out the rest.
	for i := q.Skip; i < len(ordered); i++ {
		stats.returned++
		if err := emit(ordered[i].Doc); err != nil {
			return err
		}
		if q.Limit > 0 && stats.returned >= q.Limit {
			break
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Deep copy
// ---------------------------------------------------------------------------

// deepCopyMap copies a decoded document so the caller owns it outright.
func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopy(v)
	}
	return out
}

// deepCopy recurses into the containers JSON can produce. Scalars (string,
// float64, bool, nil) are immutable in Go, so they can be shared as-is.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			out[i] = deepCopy(el)
		}
		return out
	default:
		return v
	}
}
