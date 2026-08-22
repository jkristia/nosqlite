package nosqlite

// projection_test.go covers Query.Projection end to end, through the public API.
// The projection rules themselves — inclusion versus exclusion, dotted paths,
// arrays — are pinned down in internal/engine/projection_test.go; what matters
// here is that a query applies them, and where in the query it applies them.

import (
	"reflect"
	"strings"
	"testing"
)

func TestFindProjectInclusion(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{
		Filter:     map[string]any{"_id": "1"},
		Projection: map[string]any{"name": 1, "address.city": 1},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []map[string]any{{
		"_id":     "1",
		"name":    "Ada",
		"address": map[string]any{"city": "London"},
	}}
	if !reflect.DeepEqual(docs, want) {
		t.Errorf("Find() = %v, want %v", docs, want)
	}
}

func TestFindProjectExclusion(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{
		Filter:     map[string]any{"_id": "1"},
		Projection: map[string]any{"tags": 0, "address": 0, "_id": 0},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []map[string]any{{"name": "Ada", "age": float64(36)}}
	if !reflect.DeepEqual(docs, want) {
		t.Errorf("Find() = %v, want %v", docs, want)
	}
}

// A projection narrows what comes back, never what is searched: the filter and
// the sort both run against the whole document, so either may name a field the
// projection drops.
func TestFindProjectDoesNotAffectFilterOrSort(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{
		Filter:     map[string]any{"age": map[string]any{"$gte": 41}},
		Sort:       []SortKey{{Field: "age", Desc: true}},
		Projection: map[string]any{"name": 1, "_id": 0},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []map[string]any{{"name": "Grace"}, {"name": "Alan"}, {"name": "Edsger"}}
	if !reflect.DeepEqual(docs, want) {
		t.Errorf("Find() = %v, want %v", docs, want)
	}
}

// The bounded-heap path (sort + limit) copies documents out in a different
// place from the streaming one, so it gets its own case.
func TestFindProjectWithSortAndLimit(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{
		Sort:       []SortKey{{Field: "name"}},
		Limit:      2,
		Projection: map[string]any{"name": 1, "_id": 0},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []map[string]any{{"name": "Ada"}, {"name": "Alan"}}
	if !reflect.DeepEqual(docs, want) {
		t.Errorf("Find() = %v, want %v", docs, want)
	}
}

func TestForEachProject(t *testing.T) {
	c := sample(t)

	var got []map[string]any
	err := c.ForEach(Query{
		Filter:     map[string]any{"name": "Ada"},
		Projection: map[string]any{"tags": 1, "_id": 0},
	}, func(doc map[string]any) error {
		got = append(got, doc)
		return nil
	})
	if err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	want := []map[string]any{{"tags": []any{"math", "code"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ForEach documents = %v, want %v", got, want)
	}
}

// One inclusion makes the whole projection an inclusion, so the exclusion
// beside it asks for something that has already happened.
func TestFindProjectInclusionWins(t *testing.T) {
	c := sample(t)

	docs, err := c.Find(Query{
		Filter:     map[string]any{"_id": "1"},
		Projection: map[string]any{"name": 1, "age": 0},
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := []map[string]any{{"_id": "1", "name": "Ada"}}
	if !reflect.DeepEqual(docs, want) {
		t.Errorf("Find() = %v, want %v", docs, want)
	}
}

// A bad projection must fail the query rather than being ignored.
func TestFindProjectInvalid(t *testing.T) {
	c := sample(t)

	_, err := c.Find(Query{Projection: map[string]any{"address": 1, "address.city": 0}})
	if err == nil {
		t.Fatal("Find with a contradictory projection returned no error")
	}
	if !strings.Contains(err.Error(), "name the same field") {
		t.Errorf("error = %q, want it to say the two keys name the same field", err)
	}
}

// Results are the caller's outright, projected or not.
func TestFindProjectResultIsIndependent(t *testing.T) {
	c := sample(t)

	q := Query{Filter: map[string]any{"_id": "1"}, Projection: map[string]any{"address": 1}}
	docs, err := c.Find(q)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	docs[0]["address"].(map[string]any)["city"] = "Oslo"

	again, err := c.Find(q)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if city := again[0]["address"].(map[string]any)["city"]; city != "London" {
		t.Errorf("second Find saw city %v; mutating the first result reached the store", city)
	}
}
