package nosqlite

// nosqlite_test.go covers the database as a whole: open, insert, reopen,
// collections, durability options.
//
// A note on Go testing, since it has no assert library in the standard library:
// a test is a function named TestXxx(t *testing.T) in a _test.go file. You check
// things with plain `if` statements and call t.Errorf (keep going) or t.Fatalf
// (stop this test) when something is wrong. Run everything with `go test ./...`.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// tempDB opens a fresh database in a directory the test framework deletes
// afterwards. t.Helper() tells the framework to report failures at the caller's
// line rather than inside this function.
func tempDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.nsq")
	db, err := Open(path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// t.Cleanup runs when the test finishes, pass or fail.
	t.Cleanup(func() { db.Close() })
	return db
}

func mustCollection(t *testing.T, db *DB, name string) *Collection {
	t.Helper()
	c, err := db.Collection(name)
	if err != nil {
		t.Fatalf("Collection(%q): %v", name, err)
	}
	return c
}

func TestOpenCreatesFileWithHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.nsq")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db.Path() != path {
		t.Errorf("Path() = %q, want %q", db.Path(), path)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != headerSize {
		t.Errorf("new database is %d bytes, want %d (just the header)", info.Size(), headerSize)
	}

	// The header must be readable by the inspection path too.
	fh, err := WalkFile(path, func(RawRecord) error {
		t.Error("a brand new database should contain no records")
		return nil
	})
	if err != nil {
		t.Fatalf("WalkFile: %v", err)
	}
	if fh.Format != formatVersion {
		t.Errorf("format = %d, want %d", fh.Format, formatVersion)
	}
}

func TestOpenRejectsForeignFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-db.nsq")
	if err := os.WriteFile(path, []byte("this is definitely not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a non-database file")
	}
}

func TestInsertAndFind(t *testing.T) {
	db := tempDB(t)
	users := mustCollection(t, db, "users")

	id, err := users.Insert(map[string]any{"name": "Ada", "age": 36})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if len(id) != 32 {
		t.Errorf("generated _id = %q, want 32 hex characters", id)
	}
	if users.Len() != 1 {
		t.Errorf("Len() = %d, want 1", users.Len())
	}

	docs, err := users.Find(Query{})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("Find returned %d documents, want 1", len(docs))
	}
	if docs[0]["name"] != "Ada" {
		t.Errorf("name = %v, want Ada", docs[0]["name"])
	}
	if docs[0]["_id"] != id {
		t.Errorf("_id = %v, want %v", docs[0]["_id"], id)
	}
	// The documented float64 surprise.
	if age, ok := docs[0]["age"].(float64); !ok || age != 36 {
		t.Errorf("age = %#v, want float64(36)", docs[0]["age"])
	}
}

func TestInsertDoesNotMutateCallersMap(t *testing.T) {
	db := tempDB(t)
	users := mustCollection(t, db, "users")

	doc := map[string]any{"name": "Grace"}
	if _, err := users.Insert(doc); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, present := doc["_id"]; present {
		t.Error("Insert wrote _id into the caller's map; it should return the id instead")
	}
}

func TestSuppliedIDAndDuplicateRejection(t *testing.T) {
	db := tempDB(t)
	orders := mustCollection(t, db, "orders")

	id, err := orders.Insert(map[string]any{"_id": "ord-1", "total": 10})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != "ord-1" {
		t.Errorf("id = %q, want ord-1", id)
	}

	_, err = orders.Insert(map[string]any{"_id": "ord-1", "total": 20})
	if err == nil {
		t.Fatal("inserting a duplicate _id should fail")
	}
	if orders.Len() != 1 {
		t.Errorf("Len() = %d after a rejected insert, want 1", orders.Len())
	}

	// A non-string _id is a usage error.
	if _, err := orders.Insert(map[string]any{"_id": 7}); err == nil {
		t.Error("a numeric _id should be rejected")
	}
}

func TestInsertMany(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "batch")

	docs := make([]map[string]any, 100)
	for i := range docs {
		docs[i] = map[string]any{"n": i, "even": i%2 == 0}
	}
	ids, err := c.InsertMany(docs)
	if err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	if len(ids) != 100 {
		t.Fatalf("got %d ids, want 100", len(ids))
	}
	count, err := c.Count(map[string]any{"even": true})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 50 {
		t.Errorf("Count(even) = %d, want 50", count)
	}
}

func TestInsertManyRejectsDuplicateWithinBatch(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "batch")

	_, err := c.InsertMany([]map[string]any{
		{"_id": "a"},
		{"_id": "a"},
	})
	if err == nil {
		t.Fatal("a batch containing the same _id twice should be rejected")
	}
	if c.Len() != 0 {
		t.Errorf("Len() = %d, want 0: nothing should have been written", c.Len())
	}
}

func TestReopenReplaysEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.nsq")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	users := mustCollection(t, db, "users")
	orders := mustCollection(t, db, "orders")
	// A third collection stays empty, to prove the catalog survives on its own.
	mustCollection(t, db, "empty")

	for i := 0; i < 50; i++ {
		if _, err := users.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		if _, err := orders.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	got := reopened.Collections()
	want := []string{"empty", "orders", "users"}
	if len(got) != len(want) {
		t.Fatalf("Collections() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Collections() = %v, want %v", got, want)
		}
	}

	u := mustCollection(t, reopened, "users")
	if u.Len() != 50 {
		t.Errorf("users.Len() = %d after reopen, want 50", u.Len())
	}
	o := mustCollection(t, reopened, "orders")
	if o.Len() != 10 {
		t.Errorf("orders.Len() = %d after reopen, want 10", o.Len())
	}

	// Collection ids must be stable across restarts.
	if u.ID() != 1 || o.ID() != 2 {
		t.Errorf("collection ids = users:%d orders:%d, want 1 and 2", u.ID(), o.ID())
	}
}

func TestReopenWithFastOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fast.nsq")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	for i := 0; i < 20; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	fast, err := Open(path, WithFastOpen())
	if err != nil {
		t.Fatalf("fast open: %v", err)
	}
	defer fast.Close()
	if got := mustCollection(t, fast, "c").Len(); got != 20 {
		t.Errorf("Len() = %d after fast open, want 20", got)
	}
}

func TestSyncNeverStillDurableAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bulk.nsq")

	db, err := Open(path, WithSync(SyncNever))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	for i := 0; i < 200; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := mustCollection(t, reopened, "c").Len(); got != 200 {
		t.Errorf("Len() = %d, want 200", got)
	}
}

func TestOperationsAfterCloseFail(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "x.nsq"))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(map[string]any{"a": 1}); err == nil {
		t.Error("Insert after Close should fail")
	}
	// Close is idempotent.
	if err := db.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestCollectionNameValidation(t *testing.T) {
	db := tempDB(t)
	bad := []string{"", "has space", "has/slash", "emoji🙂"}
	for _, name := range bad {
		if _, err := db.Collection(name); err == nil {
			t.Errorf("Collection(%q) should have been rejected", name)
		}
	}
	for _, name := range []string{"users", "user_names", "user-names", "a1"} {
		if _, err := db.Collection(name); err != nil {
			t.Errorf("Collection(%q): %v", name, err)
		}
	}
}

// TestConcurrentReadersAndWriter is the property the whole append-only design
// exists for: a long scan and a stream of inserts must not interfere.
//
// Run with `go test -race` to have the Go race detector check it properly.
func TestConcurrentReadersAndWriter(t *testing.T) {
	db := tempDB(t, WithSync(SyncNever))
	c := mustCollection(t, db, "c")

	for i := 0; i < 200; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 4)

	// One writer.
	go func() {
		for i := 200; i < 400; i++ {
			if _, err := c.Insert(map[string]any{"n": i}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	// Three readers.
	for r := 0; r < 3; r++ {
		go func() {
			for i := 0; i < 20; i++ {
				docs, err := c.Find(Query{Filter: map[string]any{"n": map[string]any{"$gte": 0}}})
				if err != nil {
					done <- err
					return
				}
				// Snapshot semantics: a scan sees at least what existed when it
				// started, and never a partial document.
				if len(docs) < 200 {
					done <- fmt.Errorf("scan saw only %d documents", len(docs))
					return
				}
			}
			done <- nil
		}()
	}

	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() != 400 {
		t.Errorf("Len() = %d, want 400", c.Len())
	}
}
