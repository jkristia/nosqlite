package nosqlite

// replace_test.go covers op=3 end to end: what Replace does to the documents,
// to the index and to the file, and what replay makes of the result on reopen.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestReplaceSwapsTheDocument(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3) // _id p0..p2, name person-0..2

	n, err := c.Replace(
		map[string]any{"_id": "p1"},
		map[string]any{"name": "renamed", "extra": true},
	)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if n != 1 {
		t.Fatalf("Replace returned %d, want 1", n)
	}

	got, err := c.FindOne(map[string]any{"_id": "p1"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got["name"] != "renamed" {
		t.Errorf("name = %v, want renamed", got["name"])
	}
	if got["extra"] != true {
		t.Errorf("extra = %v, want true", got["extra"])
	}
	// The _id is carried over even though the replacement did not supply one.
	if got["_id"] != "p1" {
		t.Errorf("_id = %v, want p1", got["_id"])
	}

	// The collection still holds three documents, in their original order: a
	// replace occupies the slot it replaced rather than appending a new one.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"p0", "p1", "p2"}) {
		t.Errorf("ids after replace = %v, want [p0 p1 p2]", ids)
	}
	if c.Len() != 3 {
		t.Errorf("Len = %d, want 3", c.Len())
	}

	// The file underneath tells the other half of the story. Three documents in
	// the collection, but five records on disk: the collection was defined, three
	// were inserted, and the replace was appended rather than overwriting one of
	// them. Len() counts live documents; the file counts everything ever written.
	//
	// Payloads are compared as exact strings, which is stable because
	// encoding/json sorts map keys — note "extra" landing before "name" in the
	// replacement, which is not the order it was written in.
	want := []struct {
		op      string
		id      string
		payload string
	}{
		// The catalog record that names the collection. It carries no document,
		// so it has no _id, and it is tagged with the very collection id it is
		// announcing.
		{"define", "", `{"id":1,"name":"users"}`},

		{"insert", "p0", `{"_id":"p0","name":"person-0"}`},
		// Superseded by the replace below, and still here in full: the engine
		// never goes back and edits it.
		{"insert", "p1", `{"_id":"p1","name":"person-1"}`},
		{"insert", "p2", `{"_id":"p2","name":"person-2"}`},

		// The whole new document, not a patch, and carrying the _id it inherited
		// from the document it replaced rather than one the caller supplied.
		{"replace", "p1", `{"_id":"p1","extra":true,"name":"renamed"}`},
	}

	recs := readRecords(t, db.Path())
	if len(recs) != len(want) {
		wantOps := make([]string, len(want))
		for i, w := range want {
			wantOps[i] = w.op
		}
		t.Fatalf("file holds %d records %v, want %d %v",
			len(recs), opNames(recs), len(want), wantOps)
	}
	for i, w := range want {
		rec := recs[i]
		id, err := payloadID(rec.Payload)
		if err != nil {
			t.Errorf("record %d: payload is not JSON: %v", i, err)
			continue
		}
		if opName(rec.Op) != w.op || id != w.id || string(rec.Payload) != w.payload {
			t.Errorf("record %d at offset %d:\n got op=%-7s id=%-4q payload=%s\nwant op=%-7s id=%-4q payload=%s",
				i, rec.Offset,
				opName(rec.Op), id, rec.Payload,
				w.op, w.id, w.payload)
		}
	}
}

// TestReplaceIsWholeDocumentNotAPatch pins what "replace" means: the document
// is swapped wholesale, so fields absent from the replacement are removed
// rather than left alone.
//
// It is the only test here that would catch an implementation that merged the
// replacement into the existing document instead. Everywhere else the
// replacement sets every field the original had, so merge and replace produce
// identical output; only a replacement that is a strict subset of the original
// makes the difference observable.
func TestReplaceIsWholeDocumentNotAPatch(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	if _, err := c.Insert(map[string]any{"_id": "a", "name": "Ada", "age": 36, "city": "London"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"age": 37}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	got, err := c.FindOne(map[string]any{"_id": "a"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got["age"] != float64(37) {
		t.Errorf("age = %v, want 37", got["age"])
	}
	for _, gone := range []string{"name", "city"} {
		if v, present := got[gone]; present {
			t.Errorf("%s = %v, want it gone: Replace takes a whole document, not a patch", gone, v)
		}
	}
}

func TestReplaceRejectsChangingID(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 2)

	sizeBefore := db.Size()
	n, err := c.Replace(map[string]any{"_id": "p0"}, map[string]any{"_id": "different"})
	if !errors.Is(err, ErrImmutableID) {
		t.Fatalf("Replace with a different _id: err = %v, want ErrImmutableID", err)
	}
	if n != 0 {
		t.Errorf("returned %d, want 0", n)
	}
	// Nothing may be written: a rejected replace must not leave garbage behind.
	if db.Size() != sizeBefore {
		t.Errorf("file grew by %d bytes on a rejected replace", db.Size()-sizeBefore)
	}
	if db.DeadBytes() != 0 {
		t.Errorf("DeadBytes = %d after a rejected replace, want 0", db.DeadBytes())
	}
	if got := findIDs(t, c); !equalStrings(got, []string{"p0", "p1"}) {
		t.Errorf("ids = %v, want [p0 p1]", got)
	}

	// Supplying the SAME _id is fine — it agrees rather than changes.
	if n, err := c.Replace(map[string]any{"_id": "p0"}, map[string]any{"_id": "p0", "v": 1}); err != nil || n != 1 {
		t.Errorf("Replace with the matching _id: (%d, %v), want (1, nil)", n, err)
	}
}

func TestReplaceNoMatchIsANoOp(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 2)

	sizeBefore := db.Size()
	n, err := c.Replace(map[string]any{"_id": "nobody"}, map[string]any{"v": 1})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if n != 0 {
		t.Errorf("returned %d, want 0", n)
	}
	if db.Size() != sizeBefore {
		t.Errorf("file grew by %d bytes on a replace that matched nothing", db.Size()-sizeBefore)
	}
	if c.dirty {
		t.Error("collection marked dirty by a replace that matched nothing")
	}
}

// TestReplaceOnlyTouchesTheFirstMatch is the safety property that made the bare
// verb cap at one document.
func TestReplaceOnlyTouchesTheFirstMatch(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "team": "red"}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	n, err := c.Replace(map[string]any{"team": "red"}, map[string]any{"team": "blue"})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if n != 1 {
		t.Fatalf("returned %d, want 1 — a bare Replace must never touch more than one document", n)
	}

	still, err := c.Count(map[string]any{"team": "red"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if still != 2 {
		t.Errorf("%d documents still on team red, want 2", still)
	}
}

// TestReplaceAccountsForDeadBytes checks the number `nsq stat` reports and
// Compact will reclaim.
func TestReplaceAccountsForDeadBytes(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	if _, err := c.Insert(map[string]any{"_id": "a", "pad": "0123456789"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	db.mu.RLock()
	oldRecord := recordHeaderSize + int64(c.lengths[0])
	db.mu.RUnlock()

	if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"pad": "x"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := db.DeadBytes(); got != oldRecord {
		t.Errorf("DeadBytes = %d, want %d (the whole superseded record, header included)", got, oldRecord)
	}

	// A second replace of the same document supersedes the first replacement,
	// so dead bytes accumulate rather than being recomputed.
	db.mu.RLock()
	secondRecord := recordHeaderSize + int64(c.lengths[0])
	db.mu.RUnlock()

	if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"pad": "y"}); err != nil {
		t.Fatalf("second Replace: %v", err)
	}
	if got, want := db.DeadBytes(), oldRecord+secondRecord; got != want {
		t.Errorf("DeadBytes after two replaces = %d, want %d", got, want)
	}
}

// TestReplaceMarksCollectionDirty ties the write path to the step-3 groundwork:
// without this the next scan could take the sequential path and hand back the
// document that was just replaced.
func TestReplaceMarksCollectionDirty(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 4)

	if c.dirty {
		t.Fatal("collection dirty before any replace")
	}
	if _, err := c.Replace(map[string]any{"_id": "p0"}, map[string]any{"name": "new"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if !c.dirty {
		t.Fatal("collection not marked dirty after a replace")
	}

	// The whole point of the flag: the superseded document must not come back.
	docs, err := c.Find(Query{Filter: map[string]any{"name": "person-0"}})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("the superseded document is still visible: %v", docs)
	}
}

// TestReplaceIndexIsCopyOnWrite is the §2.1 rule: a snapshot taken before a
// replace must keep seeing the document as it was.
func TestReplaceIndexIsCopyOnWrite(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3)

	before := c.snapshot()

	if _, err := c.Replace(map[string]any{"_id": "p0"}, map[string]any{"name": "new"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// The old snapshot's arrays must be untouched — that is what makes it safe
	// for a reader to hold them with no lock while a writer replaces.
	after := c.snapshot()
	if before.offsets[0] == after.offsets[0] {
		t.Fatal("setup: the replaced slot did not move, so this proves nothing")
	}
	if &before.offsets[0] == &after.offsets[0] {
		t.Error("Replace mutated the published array in place instead of copying it")
	}

	// Reading through the stale snapshot still yields the original document.
	var seen []string
	err := c.scanRecords(before, func(_ int, payload []byte) (bool, error) {
		var probe struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload, &probe); err != nil {
			return false, err
		}
		seen = append(seen, probe.Name)
		return true, nil
	})
	if err != nil {
		t.Fatalf("scan through the pre-replace snapshot: %v", err)
	}
	if !equalStrings(seen, []string{"person-0", "person-1", "person-2"}) {
		t.Errorf("pre-replace snapshot saw %v, want the original three documents", seen)
	}
}

// TestConcurrentReplaceAndReaders is the test that justifies replaceIndex
// copying rather than assigning. Run under -race it is the only direct check
// that lock-free readers and the replace write path are actually compatible;
// an in-place `c.offsets[i] = ...` fails it.
func TestConcurrentReplaceAndReaders(t *testing.T) {
	db := tempDB(t, WithSync(SyncNever))
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 50)

	done := make(chan struct{})
	errs := make(chan error, 8)

	// Readers scan continuously while the writer replaces underneath them.
	for r := 0; r < 4; r++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				docs, err := c.Find(Query{})
				if err != nil {
					errs <- err
					return
				}
				// Every scan must see the whole collection: a replace swaps a
				// document's contents, never its existence.
				if len(docs) != 50 {
					errs <- fmt.Errorf("reader saw %d documents, want 50", len(docs))
					return
				}
			}
		}()
	}

	for i := 0; i < 100; i++ {
		if _, err := c.Replace(
			map[string]any{"_id": "p7"},
			map[string]any{"name": "rewritten", "round": i},
		); err != nil {
			close(done)
			t.Fatalf("Replace round %d: %v", i, err)
		}
	}
	close(done)

	select {
	case err := <-errs:
		t.Fatalf("reader failed: %v", err)
	default:
	}
}

// TestReplaceDeadBytesMatchScanLive ties step 4's running counter to step 2's
// offline reconstruction: the open database and a cold walk of the same file
// must agree on how much garbage is in it.
func TestReplaceDeadBytesMatchScanLive(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/replace.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c, err := db.Collection("users")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 0, "pad": "0123456789"}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	for i := 1; i <= 3; i++ {
		if _, err := c.Replace(map[string]any{"_id": "b"}, map[string]any{"v": i}); err != nil {
			t.Fatalf("Replace %d: %v", i, err)
		}
	}

	want := db.DeadBytes()
	if want == 0 {
		t.Fatal("setup: no dead bytes recorded after three replaces")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stats, err := ScanLive(path)
	if err != nil {
		t.Fatalf("ScanLive: %v", err)
	}
	if stats.Dead != want {
		t.Errorf("ScanLive reports %d dead bytes, the live counter said %d", stats.Dead, want)
	}
	total := 0
	for _, n := range stats.Documents {
		total += n
	}
	if total != 3 {
		t.Errorf("ScanLive counted %d live documents, want 3", total)
	}
}

// ---------------------------------------------------------------------------
// Replay (op=3)
// ---------------------------------------------------------------------------

// reopen closes db and opens the same path again.
func reopen(t *testing.T, db *DB, path string, opts ...Option) *DB {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fresh, err := Open(path, opts...)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { fresh.Close() })
	return fresh
}

func TestReplaySurvivesReplace(t *testing.T) {
	path := t.TempDir() + "/replay.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := mustCollection(t, db, "users")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 0}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	if _, err := c.Replace(map[string]any{"_id": "b"}, map[string]any{"v": 99}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	wantDead := db.DeadBytes()

	db = reopen(t, db, path)
	c = mustCollection(t, db, "users")

	if got := c.Len(); got != 3 {
		t.Errorf("Len after reopen = %d, want 3", got)
	}
	got, err := c.FindOne(map[string]any{"_id": "b"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got == nil || got["v"] != float64(99) {
		t.Errorf("b after reopen = %v, want v=99", got)
	}
	// The superseded version must not come back, and order must be preserved.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"a", "b", "c"}) {
		t.Errorf("ids after reopen = %v, want [a b c]", ids)
	}
	if !c.dirty {
		t.Error("collection not marked dirty after replaying a replace — scans may take the sequential path")
	}
	if got := db.DeadBytes(); got != wantDead {
		t.Errorf("DeadBytes after reopen = %d, want %d", got, wantDead)
	}
}

// TestReplayInsertAfterReplaceStaysResolvable is the trap in step 5: once a
// collection's idTable has been built mid-replay, later inserts must be added to
// it, or a replace naming one of them cannot resolve and Open fails.
func TestReplayInsertAfterReplaceStaysResolvable(t *testing.T) {
	path := t.TempDir() + "/order.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := mustCollection(t, db, "users")

	// Interleave deliberately: insert, replace (builds the table), insert,
	// replace the document inserted AFTER the table existed.
	if _, err := c.Insert(map[string]any{"_id": "first", "v": 1}); err != nil {
		t.Fatalf("Insert first: %v", err)
	}
	if _, err := c.Replace(map[string]any{"_id": "first"}, map[string]any{"v": 2}); err != nil {
		t.Fatalf("Replace first: %v", err)
	}
	if _, err := c.Insert(map[string]any{"_id": "second", "v": 1}); err != nil {
		t.Fatalf("Insert second: %v", err)
	}
	if _, err := c.Replace(map[string]any{"_id": "second"}, map[string]any{"v": 42}); err != nil {
		t.Fatalf("Replace second: %v", err)
	}

	db = reopen(t, db, path)
	c = mustCollection(t, db, "users")

	got, err := c.FindOne(map[string]any{"_id": "second"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got == nil || got["v"] != float64(42) {
		t.Errorf("second after reopen = %v, want v=42", got)
	}
}

// TestReplayReplaceUnderFastOpen guards the other half: WithFastOpen skips
// payloads, but a replace record's payload is the only place its _id appears.
func TestReplayReplaceUnderFastOpen(t *testing.T) {
	path := t.TempDir() + "/fast.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := mustCollection(t, db, "users")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 0}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"v": 7}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	// An insert after the replace, so the fast path also has to keep the
	// idTable current for records whose payload it would otherwise skip.
	if _, err := c.Insert(map[string]any{"_id": "d", "v": 0}); err != nil {
		t.Fatalf("Insert d: %v", err)
	}
	if _, err := c.Replace(map[string]any{"_id": "d"}, map[string]any{"v": 8}); err != nil {
		t.Fatalf("Replace d: %v", err)
	}

	db = reopen(t, db, path, WithFastOpen())
	c = mustCollection(t, db, "users")

	for id, want := range map[string]float64{"a": 7, "d": 8, "b": 0, "c": 0} {
		got, err := c.FindOne(map[string]any{"_id": id})
		if err != nil {
			t.Fatalf("FindOne %s: %v", id, err)
		}
		if got == nil || got["v"] != want {
			t.Errorf("%s after fast reopen = %v, want v=%v", id, got, want)
		}
	}
}

// TestReplayReplaceIsIdempotentAcrossReopens checks the numbers stay stable when
// a file is opened, replaced in, and reopened repeatedly.
func TestReplayReplaceIsIdempotentAcrossReopens(t *testing.T) {
	path := t.TempDir() + "/repeat.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := mustCollection(t, db, "users")
	if _, err := c.Insert(map[string]any{"_id": "a", "v": 0}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for round := 1; round <= 4; round++ {
		if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"v": round}); err != nil {
			t.Fatalf("Replace round %d: %v", round, err)
		}
		liveDead := db.DeadBytes()

		db = reopen(t, db, path)
		c = mustCollection(t, db, "users")

		if got := db.DeadBytes(); got != liveDead {
			t.Errorf("round %d: DeadBytes after reopen = %d, want %d", round, got, liveDead)
		}
		if got := c.Len(); got != 1 {
			t.Errorf("round %d: Len = %d, want 1", round, got)
		}
		doc, err := c.FindOne(map[string]any{"_id": "a"})
		if err != nil {
			t.Fatalf("round %d: FindOne: %v", round, err)
		}
		if doc == nil || doc["v"] != float64(round) {
			t.Errorf("round %d: got %v, want v=%d", round, doc, round)
		}
	}
}

// TestReplaceThenReopenAgreesWithScanLive ties the replayed counter back to the
// offline reconstruction one more time, now that both sides can see the file.
func TestReplaceThenReopenAgreesWithScanLive(t *testing.T) {
	path := t.TempDir() + "/agree.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c := mustCollection(t, db, "users")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "pad": "0123456789"}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Replace(map[string]any{"_id": "c"}, map[string]any{"v": i}); err != nil {
			t.Fatalf("Replace %d: %v", i, err)
		}
	}

	db = reopen(t, db, path)
	replayed := db.DeadBytes()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stats, err := ScanLive(path)
	if err != nil {
		t.Fatalf("ScanLive: %v", err)
	}
	if stats.Dead != replayed {
		t.Errorf("replay computed %d dead bytes, ScanLive says %d", replayed, stats.Dead)
	}
}

// ---------------------------------------------------------------------------
// Reading the file back directly
// ---------------------------------------------------------------------------

// fileRecord is one record as it actually sits in the file.
type fileRecord struct {
	Op      uint8
	Coll    uint16
	Offset  int64
	Payload []byte
}

// readRecords walks the database file and returns every record in file order.
//
// This goes underneath the whole engine — no index, no collection, no query —
// so it is the only way to assert what is physically on disk rather than what
// the in-memory index says is there.
func readRecords(t *testing.T, path string) []fileRecord {
	t.Helper()
	var out []fileRecord
	_, err := WalkFile(path, func(r RawRecord) error {
		if !r.CRCOK {
			t.Errorf("record at offset %d failed its CRC", r.Offset)
		}
		out = append(out, fileRecord{
			Op:     r.Op,
			Coll:   r.Coll,
			Offset: r.Offset,
			// WalkFile reuses one payload buffer across calls, so this has to
			// be copied out rather than retained.
			Payload: append([]byte(nil), r.Payload...),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkFile(%s): %v", path, err)
	}
	return out
}

// opNames renders the ops of a record list, so a failure reads as
// [define insert insert] rather than [4 1 1].
func opNames(recs []fileRecord) []string {
	names := make([]string, len(recs))
	for i, r := range recs {
		names[i] = opName(r.Op)
	}
	return names
}

// TestReplaceLeavesTheOldRecordOnDisk is the append-only claim, checked against
// the bytes rather than against the API: after a replace the file holds BOTH
// versions, the superseded one untouched at the offset it always had.
func TestReplaceLeavesTheOldRecordOnDisk(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3)

	before := readRecords(t, db.Path())
	if got, want := opNames(before), []string{"define", "insert", "insert", "insert"}; !equalStrings(got, want) {
		t.Fatalf("before the replace the file holds %v, want %v", got, want)
	}
	oldRecord := before[2] // the insert of p1

	if _, err := c.Replace(map[string]any{"_id": "p1"}, map[string]any{"name": "renamed"}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	after := readRecords(t, db.Path())

	// Every record that existed before is still there, at the same offset, with
	// the same bytes. This is what "nothing is overwritten in place" means.
	for i, old := range before {
		now := after[i]
		if now.Offset != old.Offset || now.Op != old.Op || !bytes.Equal(now.Payload, old.Payload) {
			t.Errorf("record %d changed: was op=%s off=%d %q, now op=%s off=%d %q",
				i, opName(old.Op), old.Offset, old.Payload,
				opName(now.Op), now.Offset, now.Payload)
		}
	}

	// The replacement is appended after them.
	if len(after) != len(before)+1 {
		t.Fatalf("file holds %d records after the replace, want %d", len(after), len(before)+1)
	}
	newRecord := after[len(after)-1]
	if newRecord.Op != opReplace {
		t.Errorf("last record is %s, want replace", opName(newRecord.Op))
	}
	if newRecord.Offset <= oldRecord.Offset {
		t.Errorf("replacement at offset %d is not after the record it supersedes at %d",
			newRecord.Offset, oldRecord.Offset)
	}

	// Both records name p1: the dead one and the live one. Which is which is
	// knowable only from the in-memory index, never from the file.
	var mentioning []string
	for _, r := range after {
		if id, err := payloadID(r.Payload); err == nil && id == "p1" {
			mentioning = append(mentioning, opName(r.Op))
		}
	}
	if !equalStrings(mentioning, []string{"insert", "replace"}) {
		t.Errorf("records naming p1: %v, want [insert replace]", mentioning)
	}

	// And the dead one still holds the original document, byte for byte.
	if !bytes.Contains(oldRecord.Payload, []byte(`"person-1"`)) {
		t.Errorf("superseded record no longer holds the original document: %q", oldRecord.Payload)
	}
}
