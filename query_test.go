package nosqlite

// query_test.go covers the query engine: filter compilation, matching,
// cross-type ordering, sorting, skip/limit and streaming.

import (
	"errors"
	"testing"
)

// sample builds a small collection used by most of the tests below.
func sample(t *testing.T) *Collection {
	t.Helper()
	db := tempDB(t)
	c := mustCollection(t, db, "people")
	docs := []map[string]any{
		{"_id": "1", "name": "Ada", "age": 36, "tags": []any{"math", "code"}, "address": map[string]any{"city": "London"}},
		{"_id": "2", "name": "Grace", "age": 45, "tags": []any{"navy"}, "address": map[string]any{"city": "New York"}},
		{"_id": "3", "name": "Alan", "age": 41, "tags": []any{"math"}},
		{"_id": "4", "name": "Edsger", "age": 41, "address": map[string]any{"city": "Austin"}},
		{"_id": "5", "name": "Barbara", "age": nil},
	}
	if _, err := c.InsertMany(docs); err != nil {
		t.Fatalf("InsertMany: %v", err)
	}
	return c
}

// names extracts the name field of every result, for compact comparisons.
func names(docs []map[string]any) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i], _ = d["name"].(string)
	}
	return out
}
func ages(docs []map[string]any) []float64 {
	out := make([]float64, len(docs))
	for i, d := range docs {
		var ok bool
		out[i], ok = d["age"].(float64)
		if !ok {
			out[i] = 0
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
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

func equalFloats64(a, b []float64) bool {
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

func TestFilterOperators(t *testing.T) {
	c := sample(t)

	// Table-driven tests are the standard Go pattern: one slice of cases, one
	// loop, one subtest each. `go test -run TestFilterOperators/gte` runs one.
	cases := []struct {
		name   string
		filter map[string]any
		want   []string
	}{
		{"bare literal is $eq", map[string]any{"name": "Ada"}, []string{"Ada"}},
		{"gte", map[string]any{"age": map[string]any{"$gte": 41}}, []string{"Grace", "Alan", "Edsger"}},
		{"gt", map[string]any{"age": map[string]any{"$gt": 41}}, []string{"Grace"}},
		{"lt", map[string]any{"age": map[string]any{"$lt": 41}}, []string{"Ada"}},
		{"range via two operators", map[string]any{"age": map[string]any{"$gte": 36, "$lte": 41}}, []string{"Ada", "Alan", "Edsger"}},
		{"ne", map[string]any{"name": map[string]any{"$ne": "Ada"}}, []string{"Grace", "Alan", "Edsger", "Barbara"}},
		{"in", map[string]any{"name": map[string]any{"$in": []any{"Ada", "Alan"}}}, []string{"Ada", "Alan"}},
		{"nin", map[string]any{"name": map[string]any{"$nin": []any{"Ada", "Alan", "Grace"}}}, []string{"Edsger", "Barbara"}},
		{"exists true", map[string]any{"address": map[string]any{"$exists": true}}, []string{"Ada", "Grace", "Edsger"}},
		{"exists false", map[string]any{"tags": map[string]any{"$exists": false}}, []string{"Edsger", "Barbara"}},
		{"dotted path", map[string]any{"address.city": "Austin"}, []string{"Edsger"}},
		{"array contains", map[string]any{"tags": "math"}, []string{"Ada", "Alan"}},
		{"array equals whole", map[string]any{"tags": []any{"navy"}}, []string{"Grace"}},
		{"implicit and", map[string]any{"age": 41.0, "name": "Alan"}, []string{"Alan"}},
		{"explicit $and", map[string]any{"$and": []any{
			map[string]any{"age": map[string]any{"$gte": 40}},
			map[string]any{"tags": "math"},
		}}, []string{"Alan"}},
		{"$or", map[string]any{"$or": []any{
			map[string]any{"name": "Ada"},
			map[string]any{"name": "Grace"},
		}}, []string{"Ada", "Grace"}},
		{"$not", map[string]any{"$not": map[string]any{"age": map[string]any{"$gte": 41}}}, []string{"Ada", "Barbara"}},
		{"field-level $not", map[string]any{"age": map[string]any{"$not": map[string]any{"$gte": 41}}}, []string{"Ada", "Barbara"}},
		{"eq null matches null, not missing", map[string]any{"age": nil}, []string{"Barbara"}},
		{"regex as plain substring", map[string]any{"name": map[string]any{"$regex": "ac"}}, []string{"Grace"}},
		{"regex anchored prefix", map[string]any{"name": map[string]any{"$regex": "^A"}}, []string{"Ada", "Alan"}},
		{"regex case-insensitive via inline flag", map[string]any{"name": map[string]any{"$regex": "(?i)ada"}}, []string{"Ada"}},
		{"regex is case-sensitive by default", map[string]any{"name": map[string]any{"$regex": "ada"}}, []string{}},
		{"regex over an array field matches per element", map[string]any{"tags": map[string]any{"$regex": "^m"}}, []string{"Ada", "Alan"}},
		{"regex on non-string field never matches", map[string]any{"age": map[string]any{"$regex": "41"}}, []string{}},
		{"empty filter matches everything", nil, []string{"Ada", "Grace", "Alan", "Edsger", "Barbara"}},
		// Comparison operators only match within a type: 36 never matches "36".
		{"no cross-type comparison", map[string]any{"name": map[string]any{"$gte": 0}}, []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := c.Find(Query{Filter: tc.filter})
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if got := names(docs); !equalStrings(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnknownOperatorIsAnError(t *testing.T) {
	c := sample(t)
	bad := []map[string]any{
		{"age": map[string]any{"$gtee": 30}},  // typo
		{"$nope": []any{}},                    // unknown top-level operator
		{"age": map[string]any{"$exists": 1}}, // wrong argument type
		{"a": map[string]any{"$gt": 1, "b": 2}},
		{"name": map[string]any{"$regex": "("}}, // invalid pattern: unbalanced paren
		{"name": map[string]any{"$regex": 123}}, // wrong argument type
	}
	for _, filter := range bad {
		if _, err := c.Find(Query{Filter: filter}); err == nil {
			t.Errorf("filter %v should have been rejected", filter)
		}
	}
}

func TestSortSkipLimit(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{Sort: []SortKey{{Field: "age", Desc: true}}})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// null sorts before numbers, so Barbara (age: null) comes LAST descending.
	want := []string{"Grace", "Alan", "Edsger", "Ada", "Barbara"}
	if got := names(docs); !equalStrings(got, want) {
		t.Errorf("descending by age: got %v, want %v", got, want)
	}

	docs, err = c.Find(Query{Sort: []SortKey{{Field: "age"}}})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want = []string{"Barbara", "Ada", "Alan", "Edsger", "Grace"}
	if got := names(docs); !equalStrings(got, want) {
		t.Errorf("ascending by age: got %v, want %v", got, want)
	}
	ages := ages(docs)
	expectedAges := []float64{0, 36, 41, 41, 45} // null is represented as 0 in ages()
	if !equalFloats64(ages, expectedAges) {
		t.Errorf("ascending by age: got ages %v, want %v", ages, expectedAges)
	}

	// Sort + limit goes through the bounded heap.
	docs, err = c.Find(Query{Sort: []SortKey{{Field: "age", Desc: true}}, Limit: 2})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got := names(docs); !equalStrings(got, []string{"Grace", "Alan"}) {
		t.Errorf("top 2 by age: got %v", got)
	}

	// Skip is applied after ordering.
	docs, err = c.Find(Query{Sort: []SortKey{{Field: "age", Desc: true}}, Skip: 1, Limit: 2})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got := names(docs); !equalStrings(got, []string{"Alan", "Edsger"}) {
		t.Errorf("skip 1 take 2: got %v", got)
	}

	// Unsorted skip/limit walks insertion order.
	docs, err = c.Find(Query{Skip: 1, Limit: 2})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got := names(docs); !equalStrings(got, []string{"Grace", "Alan"}) {
		t.Errorf("unsorted skip/limit: got %v", got)
	}
}

func TestSecondarySortKey(t *testing.T) {
	c := sample(t)
	docs, err := c.Find(Query{Sort: []SortKey{
		{Field: "age", Desc: true},
		{Field: "name"},
	}})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	// Alan and Edsger are both 41; the second key breaks the tie by name.
	want := []string{"Grace", "Alan", "Edsger", "Ada", "Barbara"}
	if got := names(docs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFindOneAndCount(t *testing.T) {
	c := sample(t)

	doc, err := c.FindOne(map[string]any{"name": "Grace"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc == nil || doc["_id"] != "2" {
		t.Errorf("FindOne returned %v", doc)
	}

	doc, err = c.FindOne(map[string]any{"name": "Nobody"})
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc != nil {
		t.Errorf("FindOne on no match returned %v, want nil", doc)
	}

	n, err := c.Count(map[string]any{"age": map[string]any{"$gte": 41}})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}

	// An empty filter is answered from the index.
	n, err = c.Count(nil)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 5 {
		t.Errorf("Count(nil) = %d, want 5", n)
	}
}

func TestForEachAndErrStop(t *testing.T) {
	c := sample(t)

	var seen []string
	err := c.ForEach(Query{}, func(doc map[string]any) error {
		seen = append(seen, doc["name"].(string))
		if len(seen) == 2 {
			return ErrStop
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if !equalStrings(seen, []string{"Ada", "Grace"}) {
		t.Errorf("ForEach saw %v", seen)
	}

	// Any other error propagates.
	sentinel := errors.New("boom")
	err = c.ForEach(Query{}, func(map[string]any) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("ForEach error = %v, want %v", err, sentinel)
	}
}

func TestResultsAreDeepCopies(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{Filter: map[string]any{"name": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	// Mutating a result must not affect anything the engine holds.
	docs[0]["name"] = "MUTATED"
	docs[0]["address"].(map[string]any)["city"] = "MUTATED"

	again, err := c.Find(Query{Filter: map[string]any{"name": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("expected Ada to still be findable, got %d results", len(again))
	}
	if again[0]["address"].(map[string]any)["city"] != "London" {
		t.Error("mutating a result corrupted the stored document")
	}
}

func TestArrayPathTraversal(t *testing.T) {
	db := tempDB(t)
	c := mustCollection(t, db, "orders")
	if _, err := c.Insert(map[string]any{
		"_id": "o1",
		"items": []any{
			map[string]any{"sku": "a", "qty": 1},
			map[string]any{"sku": "b", "qty": 5},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// "items.qty" reaches both 1 and 5; a match on either is a match.
	got, err := c.Count(map[string]any{"items.qty": map[string]any{"$gte": 5}})
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("implicit array traversal: got %d, want 1", got)
	}

	// A numeric segment indexes the array explicitly.
	got, err = c.Count(map[string]any{"items.0.sku": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("indexed path: got %d, want 1", got)
	}
}

// The cross-type total order is tested directly in internal/engine, where the
// comparison function and the "absent" sentinel are visible. What is checked
// here is the behaviour that order produces through the public API — see the
// null-sorts-first case in TestSortSkipLimit above.

func TestQueryValidation(t *testing.T) {
	c := sample(t)
	if _, err := c.Find(Query{Skip: -1}); err == nil {
		t.Error("negative Skip should be rejected")
	}
	if _, err := c.Find(Query{Limit: -1}); err == nil {
		t.Error("negative Limit should be rejected")
	}
	if _, err := c.Find(Query{Sort: []SortKey{{Field: ""}}}); err == nil {
		t.Error("an empty sort field should be rejected")
	}
}

// TestBothReadShapesAgree exercises the strided path (a small collection
// sharing a file with a large one) and checks it returns the same thing the
// sequential path does.
func TestBothReadShapesAgree(t *testing.T) {
	db := tempDB(t, WithSync(SyncNever))
	big := mustCollection(t, db, "big")
	small := mustCollection(t, db, "small")

	// Interleave, so the small collection's records are scattered through the
	// file rather than contiguous.
	for i := 0; i < 500; i++ {
		if _, err := big.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
		if i%50 == 0 {
			if _, err := small.Insert(map[string]any{"n": i, "small": true}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// small holds 10 of 510 records -> well under the ratio, so this is the
	// strided path.
	docs, err := small.Find(Query{Sort: []SortKey{{Field: "n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 10 {
		t.Fatalf("small.Find returned %d documents, want 10", len(docs))
	}
	for i, d := range docs {
		if d["n"].(float64) != float64(i*50) {
			t.Errorf("document %d has n=%v", i, d["n"])
		}
		if d["small"] != true {
			t.Errorf("document %d leaked from the other collection: %v", i, d)
		}
	}

	// big holds 500 of 510 -> the sequential path.
	if n, err := big.Count(map[string]any{"n": map[string]any{"$gte": 0}}); err != nil || n != 500 {
		t.Errorf("big.Count = %d (err %v), want 500", n, err)
	}
}
