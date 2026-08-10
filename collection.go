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

// encodeDocument validates the document's _id (generating one if absent) and
// marshals it to JSON.
//
// The caller's map is never modified: when an _id has to be added, a shallow
// copy is made first. (Nested values are shared with the caller's map, but they
// are only read, and the result of the marshal is an independent byte slice.)
func encodeDocument(doc map[string]any) (payload []byte, id string, supplied bool, err error) {
	raw, present := doc["_id"]
	if present {
		s, ok := raw.(string)
		if !ok {
			return nil, "", false, fmt.Errorf("nosqlite: _id must be a string, got %T", raw)
		}
		if s == "" {
			return nil, "", false, errors.New("nosqlite: _id must not be empty")
		}
		id, supplied = s, true
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
