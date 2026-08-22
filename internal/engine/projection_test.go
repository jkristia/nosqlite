package engine

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// doc is the one document every Apply case below is projected from. It has a
// subdocument, an array of scalars and an array of subdocuments, which are the
// three shapes a path can run into.
const docJSON = `{
	"_id": "1",
	"name": "Ada",
	"age": 36,
	"address": {"city": "London", "zip": "EC1A 1BB", "geo": {"lat": 51.5, "lon": -0.1}},
	"tags": ["code", "math"],
	"items": [{"sku": "a", "qty": 2}, {"sku": "b", "qty": 5}, "loose"]
}`

func parse(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return m
}

func TestProjectionApply(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]any
		want string
	}{
		{
			name: "no projection returns the whole document",
			spec: nil,
			want: docJSON,
		},
		{
			name: "inclusion keeps _id without being asked",
			spec: map[string]any{"name": 1},
			want: `{"_id": "1", "name": "Ada"}`,
		},
		{
			name: "inclusion can drop _id explicitly",
			spec: map[string]any{"name": 1, "_id": 0},
			want: `{"name": "Ada"}`,
		},
		{
			name: "_id alone is an inclusion of nothing else",
			spec: map[string]any{"_id": 1},
			want: `{"_id": "1"}`,
		},
		{
			name: "_id: 0 alone is an exclusion of nothing else",
			spec: map[string]any{"_id": 0},
			want: `{
				"name": "Ada", "age": 36,
				"address": {"city": "London", "zip": "EC1A 1BB", "geo": {"lat": 51.5, "lon": -0.1}},
				"tags": ["code", "math"],
				"items": [{"sku": "a", "qty": 2}, {"sku": "b", "qty": 5}, "loose"]
			}`,
		},
		{
			name: "exclusion drops the named fields and keeps the rest",
			spec: map[string]any{"address": 0, "items": 0, "tags": 0},
			want: `{"_id": "1", "name": "Ada", "age": 36}`,
		},
		{
			name: "a dotted path rebuilds a partial subdocument",
			spec: map[string]any{"address.city": 1},
			want: `{"_id": "1", "address": {"city": "London"}}`,
		},
		{
			name: "two dotted paths share their prefix",
			spec: map[string]any{"address.city": 1, "address.geo.lat": 1, "_id": 0},
			want: `{"address": {"city": "London", "geo": {"lat": 51.5}}}`,
		},
		{
			name: "a dotted exclusion keeps the rest of the subdocument",
			spec: map[string]any{"address.zip": 0, "address.geo": 0},
			want: `{
				"_id": "1", "name": "Ada", "age": 36,
				"address": {"city": "London"},
				"tags": ["code", "math"],
				"items": [{"sku": "a", "qty": 2}, {"sku": "b", "qty": 5}, "loose"]
			}`,
		},
		{
			name: "the shorter path wins over a longer one",
			spec: map[string]any{"address": 1, "address.city": 1, "_id": 0},
			want: `{"address": {"city": "London", "zip": "EC1A 1BB", "geo": {"lat": 51.5, "lon": -0.1}}}`,
		},
		{
			name: "an array of subdocuments is projected element-wise",
			spec: map[string]any{"items.qty": 1, "_id": 0},
			want: `{"items": [{"qty": 2}, {"qty": 5}]}`,
		},
		{
			name: "excluding through an array keeps the non-document elements",
			spec: map[string]any{"items.sku": 0, "address": 0, "tags": 0, "name": 0, "age": 0},
			want: `{"_id": "1", "items": [{"qty": 2}, {"qty": 5}, "loose"]}`,
		},
		{
			name: "a projected field the document lacks stays absent",
			spec: map[string]any{"name": 1, "nickname": 1},
			want: `{"_id": "1", "name": "Ada"}`,
		},
		{
			name: "a present field with an absent subfield yields an empty subdocument",
			spec: map[string]any{"address.country": 1, "_id": 0},
			want: `{"address": {}}`,
		},
		{
			name: "a scalar cannot carry a longer path",
			spec: map[string]any{"name.first": 1},
			want: `{"_id": "1"}`,
		},
		{
			name: "excluding a field the document lacks changes nothing else",
			spec: map[string]any{"nickname": 0, "address": 0, "items": 0, "tags": 0},
			want: `{"_id": "1", "name": "Ada", "age": 36}`,
		},
		{
			// An inclusion already returns only what it names, so the
			// exclusion has nothing left to drop.
			name: "an exclusion beside an inclusion is ignored",
			spec: map[string]any{"name": 1, "age": 0},
			want: `{"_id": "1", "name": "Ada"}`,
		},
		{
			name: "sibling paths under one field: only the included one survives",
			spec: map[string]any{"address.city": 1, "address.zip": 0},
			want: `{"_id": "1", "address": {"city": "London"}}`,
		},
		{
			name: "an ignored exclusion still cannot resurrect _id: 0",
			spec: map[string]any{"name": 1, "age": 0, "_id": 0},
			want: `{"name": "Ada"}`,
		},
		{
			name: "true and false work as well as 1 and 0",
			spec: map[string]any{"name": true, "_id": false},
			want: `{"name": "Ada"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := CompileProjection(tc.spec)
			if err != nil {
				t.Fatalf("CompileProjection(%v): %v", tc.spec, err)
			}
			got := p.Apply(parse(t, docJSON))
			want := parse(t, tc.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Apply() =\n  %v\nwant\n  %v", got, want)
			}
		})
	}
}

// The result must be the caller's outright: mutating it, at any depth, must not
// reach back into the document the scan is about to reuse.
func TestProjectionApplyCopiesDeeply(t *testing.T) {
	for _, spec := range []map[string]any{
		nil,
		{"address": 1},
		{"name": 0},
	} {
		src := parse(t, docJSON)
		p, err := CompileProjection(spec)
		if err != nil {
			t.Fatalf("CompileProjection(%v): %v", spec, err)
		}
		out := p.Apply(src)

		addr := out["address"].(map[string]any)
		addr["city"] = "Oslo"
		addr["geo"].(map[string]any)["lat"] = 0.0

		orig := src["address"].(map[string]any)
		if orig["city"] != "London" {
			t.Errorf("project %v: mutating the result changed the source document", spec)
		}
		if orig["geo"].(map[string]any)["lat"] != 51.5 {
			t.Errorf("project %v: mutating a nested value changed the source document", spec)
		}
	}
}

func TestCompileProjectionEmpty(t *testing.T) {
	for _, spec := range []map[string]any{nil, {}} {
		p, err := CompileProjection(spec)
		if err != nil {
			t.Fatalf("CompileProjection(%v): %v", spec, err)
		}
		if p != nil {
			t.Errorf("CompileProjection(%v) = %v, want nil (no projection)", spec, p)
		}
	}
}

func TestCompileProjectionErrors(t *testing.T) {
	tests := []struct {
		name string
		spec map[string]any
		want string // substring the message must contain
	}{
		{
			name: "excluding a field that is also included",
			spec: map[string]any{"address": 1, "address.city": 0},
			want: `includes "address" and excludes "address.city"`,
		},
		{
			name: "excluding the parent of an included field",
			spec: map[string]any{"address.city": 1, "address": 0},
			want: `includes "address.city" and excludes "address"`,
		},
		{
			name: "$slice is not supported",
			spec: map[string]any{"tags": map[string]any{"$slice": 2.0}},
			want: "$slice",
		},
		{
			name: "a non-numeric value",
			spec: map[string]any{"name": "yes"},
			want: "takes 1 or 0",
		},
		{
			name: "an empty field name",
			spec: map[string]any{"": 1},
			want: "empty field name",
		},
		{
			name: "an empty path segment",
			spec: map[string]any{"address..city": 1},
			want: "empty segment",
		},
		{
			name: "an operator where a field belongs",
			spec: map[string]any{"$or": 1},
			want: "looks like an operator",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileProjection(tc.spec)
			if err == nil {
				t.Fatalf("CompileProjection(%v) = nil error, want one containing %q", tc.spec, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
