package nosqlite

// collection.go is the public read/write API of a collection: the methods you
// actually call. The machinery they sit on top of lives in store.go (appending),
// index.go (the in-memory index) and scan.go (query execution).

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// Insert stores one document and returns its _id.
//
// If doc has no "_id" key, one is generated. Insert does NOT modify the map you
// pass in — if you want the id in your own map, assign the returned value.
//
// A caller-supplied _id must be a non-empty string and must be unique within
// the collection; the first time you supply one, the collection builds its _id
// table, which costs one pass over the existing documents.
func (c *Collection) Insert(doc map[string]any) (string, error) {
	if doc == nil {
		return "", errors.New("nosqlite: Insert: document is nil")
	}
	payload, id, supplied, err := encodeDocument(doc)
	if err != nil {
		return "", err
	}
	return c.insertPayload(payload, id, supplied)
}

// InsertJSON stores a document supplied as raw JSON bytes.
//
// The JSON is decoded and re-encoded rather than stored verbatim: that
// validates it, and it is where a missing _id gets filled in.
func (c *Collection) InsertJSON(raw []byte) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("nosqlite: InsertJSON: %w", err)
	}
	if doc == nil {
		return "", errors.New("nosqlite: InsertJSON: document is null")
	}
	return c.Insert(doc)
}

// InsertMany stores several documents with a single write and a single fsync,
// which is what makes bulk loading fast without giving up durability.
//
// It is NOT atomic: a crash can leave a prefix of the batch on disk. The
// returned slice holds the ids that were actually written, so len(ids) tells
// you how far it got when err is non-nil.
func (c *Collection) InsertMany(docs []map[string]any) ([]string, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	payloads := make([][]byte, len(docs))
	ids := make([]string, len(docs))
	supplied := make([]bool, len(docs))

	// Encode everything first: a bad document should fail before anything is
	// written, not halfway through.
	for i, doc := range docs {
		if doc == nil {
			return nil, fmt.Errorf("nosqlite: InsertMany: document %d is nil", i)
		}
		payload, id, sup, err := encodeDocument(doc)
		if err != nil {
			return nil, fmt.Errorf("nosqlite: InsertMany: document %d: %w", i, err)
		}
		payloads[i], ids[i], supplied[i] = payload, id, sup
	}

	started := time.Now()

	db := c.db
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, ErrClosed
	}

	// Duplicate checking for any caller-supplied ids, including duplicates
	// within the batch itself.
	seen := make(map[string]struct{})
	for i, id := range ids {
		if !supplied[i] {
			continue
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w: %q appears twice in the batch", ErrDuplicateID, id)
		}
		seen[id] = struct{}{}
		if err := c.ensureIDTable(); err != nil {
			return nil, err
		}
		exists, err := c.hasID(id)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateID, id)
		}
	}

	firstOff := db.size
	offs, written, writeErr := db.appendBatch(opInsert, c.id, payloads)

	// Publish the index entries for whatever actually landed.
	for i := 0; i < written; i++ {
		c.appendIndex(offs[i]+recordHeaderSize, uint32(len(payloads[i])))
		db.total++
		if c.ids != nil {
			c.ids.insert(fingerprint(ids[i]), uint32(len(c.offsets)-1))
		}
	}

	syncErr := db.syncIfNeeded()

	err := writeErr
	if err == nil {
		err = syncErr
	}
	db.traceInsertMany(c.name, written, firstOff, int(db.size-firstOff), started, err)
	return ids[:written], err
}

// insertPayload is the shared tail of Insert and InsertJSON.
func (c *Collection) insertPayload(payload []byte, id string, supplied bool) (string, error) {
	started := time.Now()

	db := c.db
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return "", ErrClosed
	}

	if supplied {
		// Building the id table is the price of caller-supplied ids; it happens
		// at most once per collection per process.
		if err := c.ensureIDTable(); err != nil {
			return "", err
		}
		exists, err := c.hasID(id)
		if err != nil {
			return "", err
		}
		if exists {
			err := fmt.Errorf("%w: %q", ErrDuplicateID, id)
			db.traceInsert(c.name, id, 0, 0, nil, started, err)
			return "", err
		}
	}

	off, total, err := db.appendRecord(opInsert, c.id, payload)
	if err != nil {
		db.traceInsert(c.name, id, 0, 0, nil, started, err)
		return "", err
	}

	// Only publish the index entry once the bytes are on their way: a reader
	// must never see an index entry pointing at a record that was not written.
	c.appendIndex(off+recordHeaderSize, uint32(len(payload)))
	db.total++
	if c.ids != nil {
		c.ids.insert(fingerprint(id), uint32(len(c.offsets)-1))
	}

	if err := db.syncIfNeeded(); err != nil {
		db.traceInsert(c.name, id, off, total, payload, started, err)
		return "", err
	}

	db.traceInsert(c.name, id, off, total, payload, started, nil)
	return id, nil
}

// Replace swaps the first document matching filter for doc, and returns how
// many documents were replaced — 1, or 0 when nothing matched.
//
// "First" means insertion order, the same order FindOne uses. There is
// deliberately no ReplaceMany: replacing several documents with one whole
// document would leave them identical apart from their _id, which is almost
// never what anyone means. MongoDB omits it for the same reason.
//
// The replacement is a WHOLE document, not a patch: fields absent from doc are
// gone afterwards. doc need not carry an _id — the replaced document's own _id
// is kept either way — but if it does, it must match, or ErrImmutableID is
// returned and nothing is written.
//
// That check is worth reaching for. An _id in doc is not a second way to pick
// the document — filter always picks — it is an assertion that filter found the
// one you meant, and it costs nothing. Leave it out and a filter that selects
// the wrong document overwrites the wrong document, silently and irreversibly.
//
// On disk this appends a new record and leaves the old one exactly where it is;
// the space it occupies is reported by DB.DeadBytes and reclaimed only by
// Compact.
func (c *Collection) Replace(filter, doc map[string]any) (int, error) {
	if doc == nil {
		return 0, errors.New("nosqlite: Replace: document is nil")
	}
	// Compile and validate before taking the lock: a bad filter or a bad _id
	// should cost no one else any waiting.
	matcher, err := CompileFilter(filter)
	if err != nil {
		return 0, err
	}
	suppliedID, err := documentID(doc)
	if err != nil {
		return 0, fmt.Errorf("nosqlite: Replace: %w", err)
	}

	started := time.Now()

	db := c.db
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return 0, ErrClosed
	}

	pos, err := c.findFirstLocked(matcher)
	if err != nil {
		return 0, err
	}
	if pos < 0 {
		db.traceReplace(c.name, "", 0, 0, nil, started, nil)
		return 0, nil
	}

	// The replaced document's own _id wins; a supplied one may only agree.
	id, err := c.idAt(pos)
	if err != nil {
		return 0, err
	}
	if suppliedID != "" && suppliedID != id {
		// Name both sides and where each came from. The caller's mistake is
		// almost never "I tried to change an _id" — it is a filter that picked
		// a document they did not mean, and the message has to say so.
		err := fmt.Errorf("%w: the filter matched document %q, but the replacement carries _id %q; one of them is wrong",
			ErrImmutableID, id, suppliedID)
		db.traceReplace(c.name, id, 0, 0, nil, started, err)
		return 0, err
	}

	payload, err := encodeReplacement(doc, id)
	if err != nil {
		return 0, err
	}

	off, total, err := db.appendRecord(opReplace, c.id, payload)
	if err != nil {
		db.traceReplace(c.name, id, 0, 0, nil, started, err)
		return 0, err
	}

	// Order matters: read the old length for the dead-byte count before the
	// slot is repointed away from it.
	db.dead += recordHeaderSize + int64(c.lengths[pos])
	c.replaceIndex(pos, off+recordHeaderSize, uint32(len(payload)))

	// From here on this collection has records in the file that the index no
	// longer points at, so scanSequential — which re-derives membership from the
	// file — would hand back the superseded document. See scan.go.
	c.dirty = true

	// The id table needs no work at all: the _id cannot change and the document
	// keeps its slot, so every fingerprint still maps where it did. That is a
	// direct consequence of _id being immutable, not a coincidence.
	//
	// db.total is likewise left alone. It counts documents, not records, and a
	// replace adds no document — it only makes the file longer than the
	// document count implies, which costs the read-shape heuristic a little
	// accuracy for OTHER collections in the same file and nothing at all for
	// this one, which is now pinned to the strided path.

	if err := db.syncIfNeeded(); err != nil {
		db.traceReplace(c.name, id, off, total, payload, started, err)
		return 0, err
	}

	db.traceReplace(c.name, id, off, total, payload, started, nil)
	return 1, nil
}

// Delete removes the first document matching filter, and returns how many
// documents were deleted — 1, or 0 when nothing matched.
//
// "First" means insertion order, the same order FindOne uses. Removing more
// than one document at a time is DeleteMany, and the split is deliberate: a
// filter that matches more than you meant is the one mistake in this API with
// no undo, so the wider operation is the one you have to type more to get.
// MongoDB separates deleteOne and deleteMany for the same reason.
//
// On disk this appends a tombstone naming the document's _id and leaves the
// document itself exactly where it was, so a delete makes the file BIGGER
// rather than smaller. Both the abandoned document and the tombstone are
// counted by DB.DeadBytes and reclaimed only by Compact.
//
// Deleting frees the _id: inserting a new document under it afterwards is
// allowed and does not collide.
func (c *Collection) Delete(filter map[string]any) (int, error) {
	return c.deleteMatching(filter, 1)
}

// DeleteMany removes every document matching filter and returns how many were
// deleted.
//
// This is the one place a Many verb takes a filter rather than a slice, and it
// is why there is no ReplaceMany: deleting many documents is genuinely one
// operation, whereas replacing many with a single document is not.
//
// It is NOT atomic. The tombstones are written together, but a crash can leave
// a prefix of them on disk, so some documents are deleted and the rest survive.
// The returned count is how many actually landed — the number to trust when err
// is non-nil.
//
// An empty filter matches everything and empties the collection. There is no
// confirmation step and no undo.
func (c *Collection) DeleteMany(filter map[string]any) (int, error) {
	return c.deleteMatching(filter, 0)
}

// deleteMatching is the shared body of Delete and DeleteMany. limit caps how
// many documents may be removed; limit <= 0 means every match.
func (c *Collection) deleteMatching(filter map[string]any, limit int) (int, error) {
	// Compile before taking the lock, as Replace does: a bad filter should cost
	// no one else any waiting.
	matcher, err := CompileFilter(filter)
	if err != nil {
		return 0, err
	}

	started := time.Now()

	db := c.db
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return 0, ErrClosed
	}

	positions, ids, err := c.findAllLocked(matcher, limit)
	if err != nil {
		return 0, err
	}
	if len(positions) == 0 {
		// A delete that matched nothing writes nothing at all. The scan above
		// has already answered "is it there", so resolving first costs nothing —
		// and a tombstone for a document that was never present would be garbage
		// Compact has to collect on behalf of a delete that deleted nothing.
		if limit == 1 {
			db.traceDelete(c.name, "", 0, 0, started, nil)
		} else {
			db.traceDeleteMany(c.name, 0, db.size, 0, started, nil)
		}
		return 0, nil
	}

	payloads := make([][]byte, len(ids))
	for i, id := range ids {
		payload, err := encodeTombstone(id)
		if err != nil {
			return 0, err
		}
		payloads[i] = payload
	}

	// One write and one fsync for the whole set, exactly as InsertMany does. A
	// single Delete goes through the same path as a batch of one, which costs
	// one small slice and saves a second write path.
	firstOff := db.size
	_, written, writeErr := db.appendBatch(opDelete, c.id, payloads)

	if written > 0 {
		// appendBatch is not atomic and writes in order, so the tombstones that
		// landed are the prefix. Only those documents may leave the index: a
		// reader must never stop seeing a document whose tombstone failed to
		// write. positions is ascending, so the prefix is too, which is what
		// removeIndex requires.
		removed := positions[:written]
		for i, pos := range removed {
			// Read the superseded record's length before the slot goes away.
			//
			// The tombstone counts as dead the moment it is written: nothing
			// ever points at it, and Compact drops it along with the document it
			// killed. That second term is what makes a delete cost MORE bytes
			// than it reclaims until the next Compact, and it is what keeps this
			// counter equal to what ScanLive reconstructs from the file alone.
			db.dead += recordHeaderSize + int64(c.lengths[pos])
			db.dead += recordHeaderSize + int64(len(payloads[i]))
		}

		c.removeIndex(removed)

		// Unlike a replace, a delete really does change the document count, and
		// db.total is a document count — the denominator of the read-shape
		// ratio in scan.go and the docs= figure traceOpen reports.
		db.total -= len(removed)

		// From here on this collection has records in the file that the index no
		// longer points at, so scanSequential — which re-derives membership from
		// the file — would hand back the deleted documents. See scan.go.
		c.dirty = true
	}

	syncErr := db.syncIfNeeded()
	err = writeErr
	if err == nil {
		err = syncErr
	}

	if limit == 1 {
		id := ""
		if written > 0 {
			id = ids[0]
		}
		db.traceDelete(c.name, id, firstOff, int(db.size-firstOff), started, err)
	} else {
		db.traceDeleteMany(c.name, written, firstOff, int(db.size-firstOff), started, err)
	}
	return written, err
}

// findFirstLocked returns the index position of the first document matching m,
// or -1 if none does.
//
// It scans exactly as a query would, but with db.mu already held for writing,
// so it goes through snapshotLocked rather than snapshot.
//
// The position it returns is an index position in both read shapes: strided
// visits index entries directly, and sequential's counter only agrees with the
// index while the two are in step — which is precisely the condition under
// which sequential is allowed to run at all.
//
// Caller must hold db.mu.
func (c *Collection) findFirstLocked(m Matcher) (int, error) {
	pos := -1
	scratch := make(map[string]any)

	err := c.scanRecords(c.snapshotLocked(), func(i int, payload []byte) (bool, error) {
		clear(scratch)
		if err := json.Unmarshal(payload, &scratch); err != nil {
			return false, fmt.Errorf("nosqlite: decoding document in %s: %w", c.name, err)
		}
		if m.Match(scratch) {
			pos = i
			return false, nil // found it; stop
		}
		return true, nil
	})
	if err != nil {
		return -1, err
	}
	return pos, nil
}

// findAllLocked returns the index positions of every document matching m,
// together with each one's _id, in insertion order. limit <= 0 means "every
// match".
//
// The _ids come back alongside the positions because the scan has already
// unmarshalled each payload to run the filter over it. Fetching them afterwards
// through idAt would be one extra ReadAt per match, for bytes that were in hand
// a moment ago.
//
// The positions are ascending and unique, which is what removeIndex requires:
// both read shapes visit index positions in order, for the reason
// findFirstLocked describes.
//
// Caller must hold db.mu.
func (c *Collection) findAllLocked(m Matcher, limit int) (positions []int, ids []string, err error) {
	scratch := make(map[string]any)

	err = c.scanRecords(c.snapshotLocked(), func(i int, payload []byte) (bool, error) {
		clear(scratch)
		if err := json.Unmarshal(payload, &scratch); err != nil {
			return false, fmt.Errorf("nosqlite: decoding document in %s: %w", c.name, err)
		}
		if !m.Match(scratch) {
			return true, nil
		}
		id, _ := scratch["_id"].(string)
		if id == "" {
			// Every document this package writes carries a string _id, so this
			// means the file came from somewhere else. Refuse rather than append
			// a tombstone that names nothing and would fail on the next Open.
			return false, fmt.Errorf("nosqlite: document at position %d in %s has no usable _id", i, c.name)
		}
		positions = append(positions, i)
		ids = append(ids, id)
		return limit <= 0 || len(positions) < limit, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return positions, ids, nil
}

// documentID returns the _id doc carries, or "" when it has none. It is an
// error for _id to be present but not a usable string.
func documentID(doc map[string]any) (string, error) {
	raw, present := doc["_id"]
	if !present {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("_id must be a string, got %T", raw)
	}
	if s == "" {
		return "", errors.New("_id must not be empty")
	}
	return s, nil
}

// encodeReplacement marshals doc with its _id forced to id.
//
// Like encodeDocument it never modifies the caller's map, copying it when the
// _id has to be added or corrected.
func encodeReplacement(doc map[string]any, id string) ([]byte, error) {
	copied := make(map[string]any, len(doc)+1)
	for k, v := range doc {
		copied[k] = v
	}
	copied["_id"] = id

	payload, err := json.Marshal(copied)
	if err != nil {
		return nil, fmt.Errorf("nosqlite: encoding replacement: %w", err)
	}
	return payload, nil
}

// encodeTombstone builds a delete record's payload: the _id and nothing else.
//
// It is a JSON document rather than a bare id string so that replay and
// ScanLive can pull the _id out with the same payloadID probe they use on every
// other record — one shape, one parser.
//
// Marshalling rather than concatenating is not fussiness: an _id is any
// non-empty string, and one containing a quote or a backslash would otherwise
// produce a record that does not parse, which surfaces as a file that will not
// open.
func encodeTombstone(id string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{"_id": id})
	if err != nil {
		return nil, fmt.Errorf("nosqlite: encoding tombstone: %w", err)
	}
	return payload, nil
}

// encodeDocument validates the document's _id (generating one if absent) and
// marshals it to JSON.
//
// The caller's map is never modified: when an _id has to be added, a shallow
// copy is made first. (Nested values are shared with the caller's map, but they
// are only read, and the result of the marshal is an independent byte slice.)
func encodeDocument(doc map[string]any) (payload []byte, id string, supplied bool, err error) {
	id, err = documentID(doc)
	if err != nil {
		return nil, "", false, fmt.Errorf("nosqlite: %w", err)
	}
	if id != "" {
		supplied = true
	} else {
		id, err = newID()
		if err != nil {
			return nil, "", false, err
		}
		copied := make(map[string]any, len(doc)+1)
		for k, v := range doc {
			copied[k] = v
		}
		copied["_id"] = id
		doc = copied
	}

	payload, err = json.Marshal(doc)
	if err != nil {
		return nil, "", false, fmt.Errorf("nosqlite: encoding document: %w", err)
	}
	return payload, id, supplied, nil
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Find runs a query and returns the matching documents.
//
// Find materialises its results, so a filter matching a million documents
// returns a million documents — the engine's small footprint says nothing about
// what the caller asks for. Set Limit, or use ForEach for large result sets.
func (c *Collection) Find(q Query) ([]map[string]any, error) {
	// Pre-size the result when the query is bounded.
	var results []map[string]any
	if q.Limit > 0 {
		results = make([]map[string]any, 0, q.Limit)
	}
	_, err := c.runQuery(q, func(doc map[string]any) error {
		results = append(results, doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// FindOne returns the first document matching filter, or nil if there is none.
//
// "First" means insertion order, since no sort is applied.
func (c *Collection) FindOne(filter map[string]any) (map[string]any, error) {
	docs, err := c.Find(Query{Filter: filter, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	return docs[0], nil
}

// Count returns how many documents match filter.
//
// An empty filter is answered from the index with no I/O at all.
func (c *Collection) Count(filter map[string]any) (int, error) {
	if len(filter) == 0 {
		return c.Len(), nil
	}
	stats, err := c.runQuery(Query{Filter: filter}, func(map[string]any) error { return nil })
	if err != nil {
		return 0, err
	}
	return stats.matched, nil
}

// ForEach streams matches to fn, retaining nothing between calls. This is the
// low-memory way to walk a large result set: memory stays flat no matter how
// many documents match.
//
// Return ErrStop from fn to stop early without ForEach reporting an error; any
// other non-nil error stops the scan and is returned as-is.
//
// Note that a query WITH a sort still has to materialise its matches before it
// can order them, so ForEach is only constant-memory for unsorted queries.
func (c *Collection) ForEach(q Query, fn func(doc map[string]any) error) error {
	_, err := c.runQuery(q, fn)
	// errors.Is unwraps wrapped errors, so ErrStop is still recognised if a
	// caller wraps it with extra context.
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}
