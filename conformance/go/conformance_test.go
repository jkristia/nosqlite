//go:build conformance

// Package nosqlite_test runs the shared conformance suite (../testdata) against
// nosqlite's public Go API.
//
// See ../../../docs/testing.md for what a conformance test is and why the
// fixtures live where they do. Every other language's conformance suite reads
// the exact same testdata/ directory and must agree with this one.
package nosqlite_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jkristia/nosqlite"
)

// caseQuery mirrors nosqlite.Query, but in a JSON-friendly shape: it names a
// dataset instead of embedding it, so many cases can share one dataset file.
type caseQuery struct {
	Dataset   string         `json:"dataset"`
	Mutations []caseMutation `json:"mutations"`
	Filter    map[string]any `json:"filter"`
	Sort      []struct {
		Field string `json:"field"`
		Desc  bool   `json:"desc"`
	} `json:"sort"`
	Skip  int `json:"skip"`
	Limit int `json:"limit"`
}

// caseMutation is one write applied between seeding the dataset and running
// the query, which is how a case covers write behaviour: the query afterwards
// observes what the write did.
type caseMutation struct {
	Op       string         `json:"op"`
	Filter   map[string]any `json:"filter"`
	Document map[string]any `json:"document"`
	// Matched is the count the operation must return. A pointer so an absent
	// key means "don't check" rather than "must be 0".
	Matched *int `json:"matched"`
	// Error, when set, means the operation must fail with a message containing
	// this text. Mutually exclusive with Matched — a failed op has no count.
	Error string `json:"error"`
}

// caseExpected is the result every binding must produce for a case's query.
//
// Fields/Docs are optional: nosqlite has no projection support, so they don't
// change the query. They let a case narrow the full documents Find() already
// returns down to a few fields, purely so expected.json stays readable
// instead of embedding whole documents.
type caseExpected struct {
	IDs    []string         `json:"ids"`
	Fields []string         `json:"fields"`
	Docs   []map[string]any `json:"docs"`
}

const testdataDir = "../testdata"

// TestConformance runs every case under testdata/cases against a fresh
// database seeded from its named dataset.
//
// A case is any directory containing a query.json, found at any depth — so
// cases/age-gte and cases/filter/comparison/age-gte are equally valid. That
// lets the fixture tree grow subdirectories for organization (by operator,
// by feature, whatever groups hundreds of cases sanely) without this runner
// changing at all: the subtest name is just the case's path relative to
// cases/, e.g. "filter/comparison/age-gte".
func TestConformance(t *testing.T) {
	casesDir := filepath.Join(testdataDir, "cases")

	err := filepath.WalkDir(casesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "query.json" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, err := filepath.Rel(casesDir, dir)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		t.Run(name, func(t *testing.T) {
			runCase(t, dir)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", casesDir, err)
	}
}

func runCase(t *testing.T, dir string) {
	t.Helper()

	var q caseQuery
	readJSON(t, filepath.Join(dir, "query.json"), &q)

	var want caseExpected
	readJSON(t, filepath.Join(dir, "expected.json"), &want)

	dataset := readJSONL(t, filepath.Join(testdataDir, "datasets", q.Dataset+".jsonl"))

	dbPath := filepath.Join(t.TempDir(), "conformance.nsq")
	db, err := nosqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	coll, err := db.Collection("docs")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	if _, err := coll.InsertMany(dataset); err != nil {
		t.Fatalf("InsertMany: %v", err)
	}

	for i, m := range q.Mutations {
		applyMutation(t, coll, i, m)
	}

	query := nosqlite.Query{Filter: q.Filter, Skip: q.Skip, Limit: q.Limit}
	for _, s := range q.Sort {
		query.Sort = append(query.Sort, nosqlite.SortKey{Field: s.Field, Desc: s.Desc})
	}

	got, err := coll.Find(query)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	gotIDs := make([]string, len(got))
	for i, doc := range got {
		gotIDs[i], _ = doc["_id"].(string)
	}

	if !idsEqual(gotIDs, want.IDs) {
		t.Errorf("ids = %v, want %v", gotIDs, want.IDs)
	}

	if len(want.Fields) > 0 {
		gotDocs := make([]map[string]any, len(got))
		for i, doc := range got {
			gotDocs[i] = projectFields(doc, want.Fields)
		}
		if !reflect.DeepEqual(gotDocs, want.Docs) {
			t.Errorf("docs = %v, want %v", gotDocs, want.Docs)
		}
	}
}

// applyMutation performs one of a case's mutations. An unknown op is a fatal
// error rather than a skip: a fixture naming an op this runner does not
// implement would otherwise pass here while testing nothing.
func applyMutation(t *testing.T, coll *nosqlite.Collection, i int, m caseMutation) {
	t.Helper()

	// Checked before the call, not in a default: arm below, so that an op no
	// runner implements cannot be mistaken for the failure m.Error expects.
	if m.Op != "replace" {
		t.Fatalf("mutations[%d]: unknown op %q", i, m.Op)
	}
	n, err := coll.Replace(m.Filter, m.Document)

	if m.Error != "" {
		if err == nil {
			t.Fatalf("mutations[%d] (%s): want an error containing %q, got none (matched %d)", i, m.Op, m.Error, n)
		}
		if !strings.Contains(err.Error(), m.Error) {
			t.Fatalf("mutations[%d] (%s): error %q does not contain %q", i, m.Op, err, m.Error)
		}
		return
	}
	if err != nil {
		t.Fatalf("mutations[%d] (%s): %v", i, m.Op, err)
	}
	if m.Matched != nil && n != *m.Matched {
		t.Fatalf("mutations[%d] (%s) matched %d documents, want %d", i, m.Op, n, *m.Matched)
	}
}

// projectFields narrows doc down to fields, so a case can check a few fields
// without expected.json embedding the whole document.
func projectFields(doc map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		out[f] = doc[f]
	}
	return out
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
}

// readJSONL reads a dataset file: one JSON document per line.
func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	defer f.Close()

	var docs []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(line, &doc); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		docs = append(docs, doc)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return docs
}

func idsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
