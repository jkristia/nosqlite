package nosqlite

// trace_test.go checks the trace file: that it appears where expected, contains
// the operations it should, and never breaks the database when it goes wrong.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTrace(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading trace file: %v", err)
	}
	return string(b)
}

func TestTraceWritesOperations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traced.nsq")

	db, err := Open(path, WithTrace(TraceAll))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "users")
	if _, err := c.Insert(map[string]any{"name": "Ada", "age": 36}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Find(Query{Filter: map[string]any{"age": map[string]any{"$gte": 30}}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	trace := readTrace(t, path+".trace")

	for _, want := range []string{"OPEN", "DEFINE", "INSERT", "SYNC", "FIND", "CLOSE"} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace file has no %s line:\n%s", want, trace)
		}
	}
	// The three numbers that explain a query's cost.
	for _, want := range []string{"scanned=1", "matched=1", "returned=1"} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace file is missing %q:\n%s", want, trace)
		}
	}
	// The byte range of the write, so a trace line leads straight to `nsq dump`.
	if !strings.Contains(trace, "off=") || !strings.Contains(trace, "len=") {
		t.Errorf("insert line is missing off=/len=:\n%s", trace)
	}
	// Every line must be a single line — that is what makes it greppable.
	for _, line := range strings.Split(strings.TrimSpace(trace), "\n") {
		if strings.Contains(line, "\n") {
			t.Error("a trace line wrapped")
		}
	}
}

func TestTraceLevels(t *testing.T) {
	dir := t.TempDir()

	// TraceWrites must not log queries.
	path := filepath.Join(dir, "writes.nsq")
	db, err := Open(path, WithTrace(TraceWrites))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	if _, err := c.Insert(map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Find(Query{}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	trace := readTrace(t, path+".trace")
	if strings.Contains(trace, "FIND") {
		t.Errorf("TraceWrites should not log queries:\n%s", trace)
	}

	// TraceVerbose includes the document payload.
	path = filepath.Join(dir, "verbose.nsq")
	db, err = Open(path, WithTrace(TraceVerbose))
	if err != nil {
		t.Fatal(err)
	}
	c = mustCollection(t, db, "c")
	if _, err := c.Insert(map[string]any{"secret": "hunter2"}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	trace = readTrace(t, path+".trace")
	if !strings.Contains(trace, "hunter2") {
		t.Errorf("TraceVerbose should include the document payload:\n%s", trace)
	}
}

func TestTraceOffWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quiet.nsq")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	if _, err := c.Insert(map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := os.Stat(path + ".trace"); !os.IsNotExist(err) {
		t.Error("tracing is off by default, so no trace file should be created")
	}
}

func TestTraceEnvironmentVariable(t *testing.T) {
	// t.Setenv restores the previous value when the test ends.
	t.Setenv("NOSQLITE_TRACE", "all")

	dir := t.TempDir()
	path := filepath.Join(dir, "env.nsq")

	// No WithTrace here: the environment variable alone must turn it on.
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	if _, err := c.Insert(map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if !strings.Contains(readTrace(t, path+".trace"), "INSERT") {
		t.Error("NOSQLITE_TRACE=all did not enable tracing")
	}
}

func TestTraceSizeCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capped.nsq")

	db, err := Open(path, WithTrace(TraceAll), WithTraceMaxBytes(400))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	for i := 0; i < 100; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	trace := readTrace(t, path+".trace")
	if !strings.Contains(trace, "TRACE TRUNCATED") {
		t.Errorf("trace should stop at the cap with a final marker:\n%s", trace)
	}
	if len(trace) > 800 {
		t.Errorf("trace grew to %d bytes despite a 400 byte cap", len(trace))
	}
	// And the database itself must be unharmed.
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := mustCollection(t, reopened, "c").Len(); got != 100 {
		t.Errorf("Len() = %d, want 100: tracing must never affect the data", got)
	}
}

func TestTraceRecordsErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "errors.nsq")

	db, err := Open(path, WithTrace(TraceWrites))
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	if _, err := c.Insert(map[string]any{"_id": "dup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(map[string]any{"_id": "dup"}); err == nil {
		t.Fatal("expected a duplicate _id error")
	}
	db.Close()

	trace := readTrace(t, path+".trace")
	if !strings.Contains(trace, "ERROR") || !strings.Contains(trace, "duplicate _id") {
		t.Errorf("the failed insert should appear in the trace:\n%s", trace)
	}
}

func TestTraceFileOverride(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.nsq")
	tracePath := filepath.Join(dir, "elsewhere.log")

	db, err := Open(dbPath, WithTrace(TraceWrites), WithTraceFile(tracePath))
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := os.Stat(tracePath); err != nil {
		t.Errorf("trace file was not written to the overridden path: %v", err)
	}
	if _, err := os.Stat(dbPath + ".trace"); !os.IsNotExist(err) {
		t.Error("the default trace path should not have been used")
	}
}
