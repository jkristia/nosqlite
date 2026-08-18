package nosqlite

// delete_test.go covers op=2 end to end: what Delete does to the documents, to
// the index and to the file, and what replay makes of the result on reopen.
//
// The property that separates these tests from replace_test.go's is that delete
// MOVES documents. A replace keeps its slot; a delete removes one, so every
// later slot shifts down and the _id table has to be remapped along with it.
// Order is therefore asserted everywhere, not just membership.

import (
	"errors"
	"fmt"
	"testing"
)

func TestDeleteRemovesTheDocument(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3) // _id p0..p2, name person-0..2

	n, err := c.Delete(map[string]any{"_id": "p1"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("Delete returned %d, want 1", n)
	}

	got, err := c.FindOne(map[string]any{"_id": "p1"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got != nil {
		t.Errorf("p1 is still findable: %v", got)
	}

	// The survivors keep their relative order and close the gap. This is the
	// half replace never had to think about: p2 used to be at position 2 and is
	// now at position 1.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"p0", "p2"}) {
		t.Errorf("ids after delete = %v, want [p0 p2]", ids)
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}
	// Count(nil) short-circuits to Len, so it is worth pinning separately.
	if got, err := c.Count(nil); err != nil || got != 2 {
		t.Errorf("Count(nil) = (%d, %v), want (2, nil)", got, err)
	}

	// The file tells the other half of the story. Two documents in the
	// collection, five records on disk: deleting a document ADDS bytes, because
	// the tombstone is appended and the original insert stays exactly where it
	// was.
	want := []struct {
		op      string
		id      string
		payload string
	}{
		{"define", "", `{"id":1,"name":"users"}`},
		{"insert", "p0", `{"_id":"p0","name":"person-0"}`},
		// Deleted, and still here in full: nothing is ever overwritten.
		{"insert", "p1", `{"_id":"p1","name":"person-1"}`},
		{"insert", "p2", `{"_id":"p2","name":"person-2"}`},

		// The tombstone. Just the _id — nobody will ever read this record to
		// answer a query, and replay needs exactly one thing from it: which
		// document died. It is a JSON document rather than a bare string so the
		// same payloadID probe works on it as on everything else.
		{"delete", "p1", `{"_id":"p1"}`},
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

// TestDeleteOnlyTouchesTheFirstMatch is the safety property that made the bare
// verb cap at one document, and the reason DeleteMany has to be typed out.
func TestDeleteOnlyTouchesTheFirstMatch(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "team": "red"}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	n, err := c.Delete(map[string]any{"team": "red"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("returned %d, want 1 — a bare Delete must never remove more than one document", n)
	}
	// The FIRST match went, in insertion order, and the rest are untouched.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"b", "c"}) {
		t.Errorf("ids = %v, want [b c]", ids)
	}
}

func TestDeleteManyRemovesEveryMatch(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	for i, team := range []string{"red", "blue", "red", "blue", "red"} {
		if _, err := c.Insert(map[string]any{"_id": fmt.Sprintf("p%d", i), "team": team}); err != nil {
			t.Fatalf("Insert p%d: %v", i, err)
		}
	}

	n, err := c.DeleteMany(map[string]any{"team": "red"})
	if err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}
	if n != 3 {
		t.Fatalf("DeleteMany returned %d, want 3", n)
	}
	// Three slots removed from the middle and both ends of the index at once;
	// the survivors keep their insertion order.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"p1", "p3"}) {
		t.Errorf("ids = %v, want [p1 p3]", ids)
	}
	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2", c.Len())
	}

	// One call, one batch: three tombstones appended in one write.
	recs := readRecords(t, db.Path())
	var tombstones []string
	for _, r := range recs {
		if r.Op == opDelete {
			id, _ := payloadID(r.Payload)
			tombstones = append(tombstones, id)
		}
	}
	if !equalStrings(tombstones, []string{"p0", "p2", "p4"}) {
		t.Errorf("tombstones name %v, want [p0 p2 p4]", tombstones)
	}
}

// TestDeleteManyEmptyFilterEmptiesTheCollection pins the documented sharp edge.
func TestDeleteManyEmptyFilterEmptiesTheCollection(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 5)

	n, err := c.DeleteMany(nil)
	if err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}
	if n != 5 {
		t.Fatalf("returned %d, want 5", n)
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d, want 0", c.Len())
	}
	if ids := findIDs(t, c); len(ids) != 0 {
		t.Errorf("ids = %v, want none", ids)
	}
}

func TestDeleteNoMatchIsANoOp(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 2)

	sizeBefore := db.Size()
	n, err := c.Delete(map[string]any{"_id": "nobody"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n != 0 {
		t.Errorf("returned %d, want 0", n)
	}
	// Not one byte: a delete resolves its match before writing anything, so a
	// delete that deleted nothing leaves no tombstone for Compact to collect.
	if db.Size() != sizeBefore {
		t.Errorf("file grew by %d bytes on a delete that matched nothing", db.Size()-sizeBefore)
	}
	if db.DeadBytes() != 0 {
		t.Errorf("DeadBytes = %d after a delete that matched nothing, want 0", db.DeadBytes())
	}
	if c.dirty {
		t.Error("collection marked dirty by a delete that matched nothing")
	}

	// The same for the plural form.
	if n, err := c.DeleteMany(map[string]any{"name": "nobody"}); err != nil || n != 0 {
		t.Errorf("DeleteMany with no match = (%d, %v), want (0, nil)", n, err)
	}
	if db.Size() != sizeBefore {
		t.Errorf("DeleteMany that matched nothing grew the file by %d bytes", db.Size()-sizeBefore)
	}
}

// TestDeleteAccountsForDeadBytes checks the number `nsq stat` reports and
// Compact will reclaim. Delete is the operation where it has two terms.
func TestDeleteAccountsForDeadBytes(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	if _, err := c.Insert(map[string]any{"_id": "a", "pad": "0123456789"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	db.mu.RLock()
	document := recordHeaderSize + int64(c.lengths[0])
	db.mu.RUnlock()

	if _, err := c.Delete(map[string]any{"_id": "a"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The abandoned document AND the tombstone that abandoned it: the tombstone
	// is garbage from the moment it is written, since nothing ever points at it
	// and Compact drops it too. `{"_id":"a"}` is 11 bytes.
	tombstone := recordHeaderSize + int64(len(`{"_id":"a"}`))
	if got, want := db.DeadBytes(), document+tombstone; got != want {
		t.Errorf("DeadBytes = %d, want %d (document %d + tombstone %d)",
			got, want, document, tombstone)
	}
}

// TestDeleteMakesTheFileBigger is the counterintuitive claim of §6.7, checked
// rather than asserted in prose.
func TestDeleteMakesTheFileBigger(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3)

	before := db.Size()
	if _, err := c.Delete(map[string]any{"_id": "p1"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if db.Size() <= before {
		t.Errorf("file went from %d to %d bytes; a delete appends a tombstone, so it must grow",
			before, db.Size())
	}
}

// TestDeleteMarksCollectionDirty ties the write path to the step-3 groundwork:
// without it the next scan could take the sequential path, which re-derives
// membership from the file and would hand back the deleted document.
func TestDeleteMarksCollectionDirty(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 4)

	if c.dirty {
		t.Fatal("collection dirty before any delete")
	}
	if _, err := c.Delete(map[string]any{"_id": "p0"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !c.dirty {
		t.Fatal("collection not marked dirty after a delete")
	}

	// The whole point of the flag: the deleted document must not come back.
	docs, err := c.Find(Query{Filter: map[string]any{"name": "person-0"}})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("the deleted document is still visible: %v", docs)
	}
}

// TestDeleteRemapsIDTable is the highest-value test in this file.
//
// The _id table maps a fingerprint to a SLOT NUMBER, and a delete shifts every
// later slot down by one. Without the remap in removeIndex, every entry after
// the deleted position points one slot too far — which is not a wrong answer
// but an out-of-range index, so the failure is a panic inside the next
// duplicate check rather than a bad lookup.
func TestDeleteRemapsIDTable(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	// Caller-supplied ids, which is what builds the table in the first place.
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 1}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	if _, err := c.Delete(map[string]any{"_id": "a"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Every surviving _id must still resolve, and they have all moved.
	for _, id := range []string{"b", "c", "d"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 2}); !errors.Is(err, ErrDuplicateID) {
			t.Errorf("Insert %q after the delete: err = %v, want ErrDuplicateID — its table entry did not follow it", id, err)
		}
	}

	// And the deleted _id must be free again: the tombstone took its entry out,
	// so re-using it is an insert, not a collision. See §6.5.
	if _, err := c.Insert(map[string]any{"_id": "a", "v": 9}); err != nil {
		t.Fatalf("re-inserting the deleted _id: %v", err)
	}
	got, err := c.FindOne(map[string]any{"_id": "a"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if got == nil || got["v"] != float64(9) {
		t.Errorf("re-inserted a = %v, want v=9", got)
	}
	// It goes to the END of the index, unlike a replace, which keeps its place.
	if ids := findIDs(t, c); !equalStrings(ids, []string{"b", "c", "d", "a"}) {
		t.Errorf("ids = %v, want [b c d a]", ids)
	}
}

// TestDeleteManyRemapsIDTable is the same check for a batch, where several
// removals shift the survivors by different amounts.
func TestDeleteManyRemapsIDTable(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	for i := 0; i < 8; i++ {
		if _, err := c.Insert(map[string]any{"_id": fmt.Sprintf("k%d", i), "even": i%2 == 0}); err != nil {
			t.Fatalf("Insert k%d: %v", i, err)
		}
	}

	// Removes k0, k2, k4, k6 — so k1 shifts down 1, k3 down 2, k5 down 3, k7 down 4.
	if n, err := c.DeleteMany(map[string]any{"even": true}); err != nil || n != 4 {
		t.Fatalf("DeleteMany = (%d, %v), want (4, nil)", n, err)
	}
	if ids := findIDs(t, c); !equalStrings(ids, []string{"k1", "k3", "k5", "k7"}) {
		t.Fatalf("ids = %v, want [k1 k3 k5 k7]", ids)
	}
	for _, id := range []string{"k1", "k3", "k5", "k7"} {
		if _, err := c.Insert(map[string]any{"_id": id}); !errors.Is(err, ErrDuplicateID) {
			t.Errorf("Insert %q: err = %v, want ErrDuplicateID", id, err)
		}
	}
	for _, id := range []string{"k0", "k2", "k4", "k6"} {
		if _, err := c.Insert(map[string]any{"_id": id}); err != nil {
			t.Errorf("re-inserting deleted %q: %v", id, err)
		}
	}
}

// TestDeleteIndexIsCopyOnWrite is the §2.1 rule: a snapshot taken before a
// delete must keep seeing every document it had, at the positions it had them.
func TestDeleteIndexIsCopyOnWrite(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 3)

	before := c.snapshot()

	if _, err := c.Delete(map[string]any{"_id": "p0"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	after := c.snapshot()
	if before.n() != 3 || after.n() != 2 {
		t.Fatalf("setup: snapshot sizes are %d then %d, want 3 then 2", before.n(), after.n())
	}
	if &before.offsets[0] == &after.offsets[0] {
		t.Error("Delete mutated the published array in place instead of copying it")
	}

	// Reading through the stale snapshot still yields all three documents in
	// their original order — which is what makes it safe for a reader to hold
	// those arrays with no lock while a writer deletes underneath it.
	var seen []string
	err := c.scanRecords(before, func(_ int, payload []byte) (bool, error) {
		id, err := payloadID(payload)
		if err != nil {
			return false, err
		}
		seen = append(seen, id)
		return true, nil
	})
	if err != nil {
		t.Fatalf("scan through the pre-delete snapshot: %v", err)
	}
	if !equalStrings(seen, []string{"p0", "p1", "p2"}) {
		t.Errorf("pre-delete snapshot saw %v, want the original three documents", seen)
	}
}

// TestConcurrentDeleteAndReaders is what justifies removeIndex copying rather
// than shifting in place. Run under -race it is the only direct check that
// lock-free readers and the delete write path are compatible.
//
// The reader assertion differs from the replace one: a delete DOES change how
// many documents there are, so the invariant is not "always 50" but "whatever
// this scan saw, it saw as a consistent prefix of the original order, with no
// duplicates and no torn documents".
func TestConcurrentDeleteAndReaders(t *testing.T) {
	db := tempDB(t, WithSync(SyncNever))
	c := mustCollection(t, db, "users")
	seedPeople(t, c, 100)

	done := make(chan struct{})
	errs := make(chan error, 8)

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
				// Every document must be whole and named, and no _id may appear
				// twice — a torn read shows up as either a decode failure inside
				// Find or a repeated id here.
				seen := make(map[string]bool, len(docs))
				for _, d := range docs {
					id, _ := d["_id"].(string)
					if id == "" {
						errs <- fmt.Errorf("reader saw a document with no _id: %v", d)
						return
					}
					if seen[id] {
						errs <- fmt.Errorf("reader saw %s twice in one scan", id)
						return
					}
					seen[id] = true
					if d["name"] == nil {
						errs <- fmt.Errorf("reader saw a truncated document: %v", d)
						return
					}
				}
			}
		}()
	}

	for i := 0; i < 100; i++ {
		n, err := c.Delete(map[string]any{"_id": fmt.Sprintf("p%d", i)})
		if err != nil {
			close(done)
			t.Fatalf("Delete p%d: %v", i, err)
		}
		if n != 1 {
			close(done)
			t.Fatalf("Delete p%d removed %d documents, want 1", i, n)
		}
	}
	close(done)

	select {
	case err := <-errs:
		t.Fatalf("reader failed: %v", err)
	default:
	}
	if c.Len() != 0 {
		t.Errorf("Len = %d after deleting every document, want 0", c.Len())
	}
}

// TestDeleteDeadBytesMatchScanLive ties the running counter to the offline
// reconstruction: the open database and a cold walk of the same file must agree
// on how much garbage is in it.
func TestDeleteDeadBytesMatchScanLive(t *testing.T) {
	path := t.TempDir() + "/delete.nsq"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c, err := db.Collection("users")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := c.Insert(map[string]any{"_id": id, "pad": "0123456789"}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	// A replace and a delete in the same file, so the two kinds of garbage are
	// summed rather than each being right on its own.
	if _, err := c.Replace(map[string]any{"_id": "a"}, map[string]any{"v": 1}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, err := c.DeleteMany(map[string]any{"pad": "0123456789"}); err != nil {
		t.Fatalf("DeleteMany: %v", err)
	}

	want := db.DeadBytes()
	if want == 0 {
		t.Fatal("setup: no dead bytes recorded")
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
	live := 0
	for _, n := range stats.Documents {
		live += n
	}
	// "a" was replaced, so its pad field is gone and DeleteMany did not match it.
	if live != 1 {
		t.Errorf("ScanLive counted %d live documents, want 1", live)
	}
}
