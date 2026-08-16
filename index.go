package nosqlite

// index.go holds the entire in-memory state of a collection.
//
// The design target is 1,000,000 documents in ~12 MB of process memory, with
// the documents themselves never all resident. Memory therefore holds only what
// is needed to *find* a record, and the minimum for that is where it starts and
// how long it is:
//
//	offsets []int64    8 bytes/doc
//	lengths []uint32   4 bytes/doc
//	                  ----
//	                  12 bytes/doc
//
// Why two parallel arrays instead of []struct{off int64; len uint32} or a map:
//
//  1. The Go garbage collector never scans them. []int64 and []uint32 contain
//     no pointers, so the GC treats each as one opaque block no matter how long
//     it is. A map[string]*entry with a million entries is a million strings
//     plus a million pointers to walk on *every* GC cycle. Being pointer-free
//     is the whole trick.
//  2. Two allocations in total, not two million.
//  3. A []struct would pad to 16 bytes per element; splitting saves 25%.

import (
	"encoding/json"
	"fmt"
	"os"
)

// Collection is a named group of documents inside the database file.
//
// Get one from DB.Collection. It is safe for concurrent use.
type Collection struct {
	db   *DB
	name string
	id   uint16 // as written into each record header

	// The whole index: one element per document, in insertion order.
	// Guarded by db.mu. See the append-only rule below.
	offsets []int64
	lengths []uint32

	// dirty is set the first time a document in this collection is replaced or
	// deleted, and cleared by Compact. Guarded by db.mu.
	//
	// It exists because scanSequential (scan.go) re-derives collection
	// membership from each record header's coll byte rather than consulting the
	// index — which is only equivalent to the index while every op=1 record this
	// collection ever wrote is still live. One replace or delete breaks that:
	// the superseded record is still in the file, still carries this
	// collection's id, and scanSequential would hand it back. A whole-collection
	// bool is blunt — one replace disables the fast path for every subsequent
	// scan — but it is one branch, it fails in the safe direction, and Compact
	// clears it. See docs/updates-and-compaction.md §2.2.
	dirty bool

	// ids is nil until something actually needs _id lookup — the first insert
	// with a caller-supplied _id. Workloads that only use generated ids (which
	// are 128-bit and cannot realistically collide) never pay for it.
	ids *idTable
}

// Name returns the collection name.
func (c *Collection) Name() string { return c.name }

// ID returns the small integer id stored in each of this collection's record
// headers. Mostly useful when correlating with `nsq dump` output.
func (c *Collection) ID() uint16 { return c.id }

// Len returns the number of documents, straight from the index. No I/O.
func (c *Collection) Len() int {
	c.db.mu.RLock()
	defer c.db.mu.RUnlock()
	return len(c.offsets)
}

// appendIndex records where a newly written document lives.
//
// THE ONE RULE that makes lock-free reads safe (see snapshot below):
//
//	once an index entry is visible to a snapshot, its value never changes again.
//
// Appending obeys it even though append can write into the very array a reader
// is holding: a snapshot caps its slices at the length they had when it was
// taken, so the element append writes — at index n — is outside every existing
// reader's view. Reallocating is equally invisible, since readers keep the old
// array.
//
// Changing an entry a reader can already see is what the rule forbids, and the
// only safe way to do it is to build new arrays and publish them under the write
// lock — see replaceIndex.
//
// Caller must hold db.mu for writing.
func (c *Collection) appendIndex(payloadOffset int64, payloadLength uint32) {
	c.offsets = append(c.offsets, payloadOffset)
	c.lengths = append(c.lengths, payloadLength)
}

// replaceIndex moves the document at position i to a newly written record.
//
// It changes an entry readers can already see, which the rule above forbids
// doing in place, so it does the only safe thing: write position i in fresh
// arrays that no reader has ever held, then publish both at once. The document's
// position in the index changes; element i of every array a reader is holding
// does not. Those readers keep describing a consistent earlier state.
//
// Assigning c.offsets[i] directly would be a data race with every lock-free
// reader, and worse than a normal one: offsets and lengths are separate arrays,
// so a reader can catch the new offset with the old length and read the wrong
// number of bytes from the right place — truncated JSON rather than a crash.
// See docs/updates-and-compaction.md §2.1.
//
// The copy is O(n) per call, which is why Replace is filter-based: one call can
// amortise it over many documents. §4 of that doc records the chunked-slice
// alternative if the cost ever shows up in a benchmark.
//
// Caller must hold db.mu for writing.
func (c *Collection) replaceIndex(i int, payloadOffset int64, payloadLength uint32) {
	offsets := make([]int64, len(c.offsets))
	copy(offsets, c.offsets)
	lengths := make([]uint32, len(c.lengths))
	copy(lengths, c.lengths)

	offsets[i] = payloadOffset
	lengths[i] = payloadLength

	c.offsets, c.lengths = offsets, lengths
}

// snapshot is a consistent point-in-time view of a collection, taken under the
// read lock and then used with no lock held at all.
//
// This works because the file is append-only: the bytes a snapshot refers to
// are immutable, so a scan can run for seconds while a writer keeps appending,
// and neither blocks the other. The visible consequence is snapshot semantics:
// documents inserted after a query started are not seen by that query.
type snapshot struct {
	// file is captured rather than reached for through c.db.file at scan time,
	// because offsets are only meaningful against the file they were taken
	// from. Today those are always the same handle; once Compact swaps in a
	// rewritten file (docs/updates-and-compaction.md §7) an in-flight scan would
	// otherwise pair old offsets with the new file and read at arbitrary record
	// boundaries. Capturing it is one field now and invasive later.
	file *os.File

	offsets []int64
	lengths []uint32
	size    int64 // file size at snapshot time
	total   int   // total insert records in the whole file, for the read-shape heuristic
	dirty   bool  // this collection has superseded records in the file; see Collection.dirty
}

// n is the number of documents in the snapshot.
func (s snapshot) n() int { return len(s.offsets) }

// snapshot copies the values a scan needs. This is the only time a reader
// touches the lock.
func (c *Collection) snapshot() snapshot {
	db := c.db
	db.mu.RLock()
	defer db.mu.RUnlock()
	return c.snapshotLocked()
}

// snapshotLocked is snapshot's body for callers that already hold db.mu — the
// write path, which needs to scan for the document it is about to replace or
// delete. It cannot call snapshot: Go's RWMutex is not reentrant, so taking the
// read lock while holding the write lock deadlocks.
func (c *Collection) snapshotLocked() snapshot {
	db := c.db
	n := len(c.offsets)
	return snapshot{
		file: db.file,
		// The three-index form s[low:high:max] caps the capacity, so an append
		// to the snapshot can never write into the live arrays. Without it,
		// `append` might reuse spare capacity that the writer is also using.
		offsets: c.offsets[0:n:n],
		lengths: c.lengths[0:n:n],
		size:    db.size,
		total:   db.total,
		dirty:   c.dirty,
	}
}

// ---------------------------------------------------------------------------
// idTable — the lazily built _id -> position map
// ---------------------------------------------------------------------------

// idTable is an open-addressed hash table from a 64-bit fingerprint of an _id
// to the document's position in the index arrays.
//
// It is also pointer-free (two numeric slices), for the same GC reason as the
// index itself. Cost is 12 bytes per slot, and the table is kept at least half
// empty, so about 24 bytes per document — which is why it is built lazily and
// only when a caller actually supplies their own _id values.
//
// A fingerprint match is *verified* by reading that one record and comparing
// the real _id string, so there are no false positives — only a rare wasted
// read.
type idTable struct {
	fingerprints []uint64 // 0 means "empty slot"
	positions    []uint32 // index into Collection.offsets
	count        int
	mask         uint64 // len(fingerprints)-1; len is always a power of two
}

// newIDTable allocates a table sized to the next power of two >= 2*capacity.
func newIDTable(capacity int) *idTable {
	size := 16
	for size < capacity*2 {
		size *= 2
	}
	return &idTable{
		fingerprints: make([]uint64, size),
		positions:    make([]uint32, size),
		mask:         uint64(size - 1),
	}
}

// fingerprint is FNV-1a, a small fast non-cryptographic hash. Written out here
// rather than using hash/fnv to avoid an interface call and an allocation per
// lookup.
//
// It never returns 0, because 0 is the "empty slot" marker.
func fingerprint(s string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	if h == 0 {
		h = 1
	}
	return h
}

// insert adds a position. It assumes the id is not already present — callers
// check first with lookup.
func (t *idTable) insert(fp uint64, pos uint32) {
	if (t.count+1)*2 > len(t.fingerprints) {
		t.grow()
	}
	// Linear probing: start at the hash slot and walk forward until an empty
	// one turns up. Cheap and cache-friendly at these load factors.
	i := fp & t.mask
	for t.fingerprints[i] != 0 {
		i = (i + 1) & t.mask
	}
	t.fingerprints[i] = fp
	t.positions[i] = pos
	t.count++
}

// forEachCandidate calls fn with the position of every entry whose fingerprint
// matches. Several documents can share a fingerprint, so fn must verify by
// reading the real _id; it returns true to stop searching.
func (t *idTable) forEachCandidate(fp uint64, fn func(pos uint32) bool) {
	i := fp & t.mask
	for t.fingerprints[i] != 0 {
		if t.fingerprints[i] == fp {
			if fn(t.positions[i]) {
				return
			}
		}
		i = (i + 1) & t.mask
	}
}

// grow doubles the table and reinserts everything.
func (t *idTable) grow() {
	oldFP, oldPos := t.fingerprints, t.positions
	size := len(oldFP) * 2
	t.fingerprints = make([]uint64, size)
	t.positions = make([]uint32, size)
	t.mask = uint64(size - 1)
	t.count = 0
	for i, fp := range oldFP {
		if fp != 0 {
			t.insert(fp, oldPos[i])
		}
	}
}

// ---------------------------------------------------------------------------
// _id lookup on the collection
// ---------------------------------------------------------------------------

// ensureIDTable builds the _id table if it does not exist yet.
//
// Building it means reading every document in the collection once — that is the
// honest cost of the first caller-supplied _id, and it is why generated ids are
// the cheaper path. Caller must hold db.mu for writing.
func (c *Collection) ensureIDTable() error {
	if c.ids != nil {
		return nil
	}
	table := newIDTable(len(c.offsets) + 16)
	for i := range c.offsets {
		id, err := c.idAt(i)
		if err != nil {
			return err
		}
		if id != "" {
			table.insert(fingerprint(id), uint32(i))
		}
	}
	c.ids = table
	return nil
}

// payloadID extracts just the _id from a document payload.
//
// json.Unmarshal into a struct with one field ignores everything else, which is
// much cheaper than building a whole map for a document we only need to name.
func payloadID(payload []byte) (string, error) {
	var probe struct {
		ID string `json:"_id"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return "", err
	}
	return probe.ID, nil
}

// idAt reads document i and returns its _id.
func (c *Collection) idAt(i int) (string, error) {
	buf := make([]byte, c.lengths[i])
	if _, err := c.db.file.ReadAt(buf, c.offsets[i]); err != nil {
		return "", fmt.Errorf("nosqlite: reading document at %d: %w", c.offsets[i], err)
	}
	id, err := payloadID(buf)
	if err != nil {
		return "", fmt.Errorf("nosqlite: decoding document at %d: %w", c.offsets[i], err)
	}
	return id, nil
}

// lookupID returns the position of the document with this _id.
//
// A fingerprint match is only a candidate: several _ids can share one, so each
// is verified by reading that document and comparing the real string. Caller
// must hold db.mu (the table and the file are both consulted).
func (c *Collection) lookupID(id string) (pos int, found bool, err error) {
	if c.ids == nil {
		return -1, false, nil
	}
	pos = -1
	c.ids.forEachCandidate(fingerprint(id), func(candidate uint32) bool {
		got, readErr := c.idAt(int(candidate))
		if readErr != nil {
			err = readErr
			return true // stop
		}
		if got == id {
			pos, found = int(candidate), true
			return true // stop
		}
		return false // fingerprint collision; keep probing
	})
	return pos, found, err
}

// hasID reports whether the collection already contains a document with this
// _id. Caller must hold db.mu.
func (c *Collection) hasID(id string) (bool, error) {
	_, found, err := c.lookupID(id)
	return found, err
}
