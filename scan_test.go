package nosqlite

// scan_test.go covers the two read shapes and the state that decides between
// them. These are internal tests (package nosqlite, not nosqlite_test) because
// the thing under test — which scan path runs — has no public surface.

import (
	"encoding/json"
	"fmt"
	"testing"
)

// seedPeople inserts n documents with predictable names and returns the ids.
func seedPeople(t *testing.T, c *Collection, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := c.Insert(map[string]any{
			"_id":  fmt.Sprintf("p%d", i),
			"name": fmt.Sprintf("person-%d", i),
		})
		if err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
		ids[i] = id
	}
	return ids
}

func findIDs(t *testing.T, c *Collection) []string {
	t.Helper()
	docs, err := c.Find(Query{})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i], _ = d["_id"].(string)
	}
	return ids
}

// TestDirtyForcesStridedScan is the regression test for
// docs/records.md
//
// scanSequential re-derives collection membership from each record header
// rather than from the index, so it returns records the index has dropped. That
// is fine while the two agree — which they do until a replace or delete
// supersedes a record.
//
// The test drops a slot from the index directly rather than calling Delete,
// because Delete sets dirty — and this test has to run the sequential path over
// an index the file disagrees with in order to show what the flag prevents.
// Removing a slot is the shape that makes the two read paths disagree about
// *which* documents come back rather than merely how many; replace, which moves
// a slot instead of removing one, is covered in replace_test.go, and the real
// Delete write path in delete_test.go.
//
// Without the dirty flag the sequential path ignores that removal and hands
// back the superseded document; with it, the strided path reads the index and
// gets it right. The test asserts BOTH halves, because a test that only checked
// the fixed behaviour would still pass if the flag were never consulted.
func TestDirtyForcesStridedScan(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "people")
	seedPeople(t, c, 5)

	// One collection owning every record puts the ratio at 1.0, well over
	// sequentialScanRatio, so scanRecords picks the sequential path.
	if snap := c.snapshot(); float64(snap.n())/float64(snap.total) < sequentialScanRatio {
		t.Fatalf("setup: expected the sequential path to be chosen, ratio was %d/%d",
			snap.n(), snap.total)
	}

	// Drop the FIRST document from the index, as a delete of p0 would. Removing
	// the first rather than the last is what makes the two paths disagree about
	// *which* documents come back, not merely how many.
	db.mu.Lock()
	c.offsets = append([]int64(nil), c.offsets[1:]...)
	c.lengths = append([]uint32(nil), c.lengths[1:]...)
	db.mu.Unlock()

	// The bug, demonstrated: sequential walks the file from the start and
	// returns the first 4 records it sees, which still includes the deleted p0.
	got := findIDs(t, c)
	want := []string{"p0", "p1", "p2", "p3"}
	if !equalStrings(got, want) {
		t.Fatalf("sequential scan over a stale index: got %v, want %v "+
			"(if this fails, scanSequential's behaviour changed and the dirty "+
			"flag may no longer be load-bearing)", got, want)
	}

	// The fix: with dirty set, the strided path reads the index and p0 is gone.
	db.mu.Lock()
	c.dirty = true
	db.mu.Unlock()

	got = findIDs(t, c)
	want = []string{"p1", "p2", "p3", "p4"}
	if !equalStrings(got, want) {
		t.Errorf("dirty collection: got %v, want %v", got, want)
	}
}

// TestScanShapesAgree pins the invariant the dirty flag protects: on a
// collection with no superseded records, both read shapes return exactly the
// same documents in the same order.
func TestScanShapesAgree(t *testing.T) {
	db := tempDB(t)
	people := mustCollection(t, db, "people")
	other := mustCollection(t, db, "other")

	seedPeople(t, people, 6)
	// Enough records in a second collection to push people below the ratio, so
	// the heuristic would pick strided on its own.
	for i := 0; i < 20; i++ {
		if _, err := other.Insert(map[string]any{"n": i}); err != nil {
			t.Fatalf("Insert into other: %v", err)
		}
	}

	snap := people.snapshot()
	if float64(snap.n())/float64(snap.total) >= sequentialScanRatio {
		t.Fatalf("setup: expected the strided path, ratio was %d/%d", snap.n(), snap.total)
	}

	var strided, sequential []string
	collect := func(dst *[]string) func(int, []byte) (bool, error) {
		return func(_ int, payload []byte) (bool, error) {
			var probe struct {
				ID string `json:"_id"`
			}
			if err := json.Unmarshal(payload, &probe); err != nil {
				return false, err
			}
			*dst = append(*dst, probe.ID)
			return true, nil
		}
	}
	if err := people.scanStrided(snap, collect(&strided)); err != nil {
		t.Fatalf("scanStrided: %v", err)
	}
	if err := people.scanSequential(snap, collect(&sequential)); err != nil {
		t.Fatalf("scanSequential: %v", err)
	}
	if !equalStrings(strided, sequential) {
		t.Errorf("read shapes disagree: strided %v, sequential %v", strided, sequential)
	}
	if len(strided) != 6 {
		t.Errorf("expected 6 documents, got %d", len(strided))
	}
}

// TestSnapshotCapturesFile guards the change that lets Compact swap the file
// out from under an in-flight scan: the scan must read through the handle the
// snapshot captured, not through whatever db.file happens to be later.
func TestSnapshotCapturesFile(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "people")
	seedPeople(t, c, 3)

	snap := c.snapshot()
	if snap.file == nil {
		t.Fatal("snapshot did not capture the file handle")
	}
	if snap.file != db.file {
		t.Errorf("snapshot captured %v, want the database's own handle %v", snap.file, db.file)
	}
}

func TestDeadBytesStartsAtZero(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "people")
	seedPeople(t, c, 4)

	if got := db.DeadBytes(); got != 0 {
		t.Errorf("DeadBytes on an insert-only database = %d, want 0", got)
	}
	if got := db.Size(); got <= headerSize {
		t.Errorf("Size = %d, want more than the %d-byte file header", got, headerSize)
	}
	if c.dirty {
		t.Error("collection is dirty after inserts only")
	}
}
