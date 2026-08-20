package nosqlite

// query.go is the public face of the query engine.
//
// The engine itself — filter compilation, the Matcher tree, path walking,
// cross-type ordering and the bounded heap — lives in internal/engine. That
// package knows nothing about files, offsets or locks: hand it a decoded
// document and it will tell you whether the document matches and how it sorts.
// Everything in this file exists so that callers never have to know that.
//
// "internal" is a directory name Go gives special meaning: a package under
// internal/ can only be imported from within the module that contains it. It is
// a compiler-enforced "private", which is what lets the engine expose whatever
// this package needs without those names becoming part of nosqlite's promise to
// its users.

import (
	"fmt"
	"strings"

	"github.com/jkristia/nosqlite/internal/engine"
)

// Query describes what to read and in what order.
//
// The zero Query — Query{} — is valid and means "everything, in insertion
// order", which is a nice property of Go's zero values.
type Query struct {
	// Filter is a Mongo-dialect filter document. nil or empty matches
	// everything. Example:
	//
	//   map[string]any{
	//       "age":  map[string]any{"$gte": 30},
	//       "$or":  []any{
	//           map[string]any{"city": "Oslo"},
	//           map[string]any{"city": "Bergen"},
	//       },
	//   }
	Filter map[string]any

	// Projection narrows the documents that come back. nil or empty returns
	// them whole.
	//
	// Every key is a field path and every value says what to do with it:
	//
	//   1 (or true)   include this field
	//   0 (or false)  exclude this field
	//
	// so a projection is either an inclusion or an exclusion, never both:
	//
	//   map[string]any{"name": 1, "address.city": 1}  // only these fields
	//   map[string]any{"email": 0, "address.zip": 0}  // everything but these
	//
	// _id is the exception to that rule: it is included by default, so it may
	// be excluded from an inclusion projection with {"_id": 0}. A dotted path
	// rebuilds a partial subdocument rather than flattening the key:
	// {"address.city": 1} yields {"address": {"city": ...}}. Array projection
	// operators ($slice, the positional $) are not supported and are rejected
	// rather than ignored.
	//
	// The projection applies at copy-out, after Filter and Sort, so both may
	// name fields it drops.
	Projection map[string]any

	// Sort keys are applied in order. Empty means insertion order, which is
	// also roughly creation order because generated _ids are time-prefixed.
	Sort []SortKey

	// Skip drops this many matches before returning any.
	Skip int

	// Limit caps the number of documents returned. 0 means no limit.
	//
	// Setting a Limit is what keeps memory bounded: with a limit the engine
	// holds at most Skip+Limit documents no matter how many match.
	Limit int
}

// SortKey is one level of an ordering: a dotted field path and a direction.
//
//	nosqlite.SortKey{Field: "address.city"}            // ascending
//	nosqlite.SortKey{Field: "age", Desc: true}         // descending
//
// The `=` makes this a type *alias* rather than a new type: SortKey and
// engine.SortKey are the same type, not two convertible ones. That is what lets
// the definition sit next to the sorting code without callers ever seeing the
// internal package.
type SortKey = engine.SortKey

// Matcher is a compiled filter — one node of the tree CompileFilter produces.
//
// Also an alias, for the same reason as SortKey.
type Matcher = engine.Matcher

// Projection is also the compiled form of Query.Projection — the same word for
// the same thing, once as the document you write and once as what the engine
// runs. The nil *Projection means "the whole document".
//
// Also an alias.
type Projection = engine.Projection

// CompileFilter turns a filter document into a Matcher tree.
//
// It is exported because it is useful on its own — for validating a filter
// before running it, and as the input an index planner would need.
func CompileFilter(filter map[string]any) (Matcher, error) {
	return engine.CompileFilter(filter)
}

// CompileProjection turns a projection document into a Projection, for the
// same reason CompileFilter is exported: checking one without running a query.
func CompileProjection(projection map[string]any) (*Projection, error) {
	return engine.CompileProjection(projection)
}

// validate checks a Query's non-filter parts.
func (q Query) validate() error {
	if q.Skip < 0 {
		return fmt.Errorf("nosqlite: Skip must not be negative (got %d)", q.Skip)
	}
	if q.Limit < 0 {
		return fmt.Errorf("nosqlite: Limit must not be negative (got %d)", q.Limit)
	}
	for _, k := range q.Sort {
		if k.Field == "" {
			return fmt.Errorf("nosqlite: sort key has an empty field name")
		}
	}
	return nil
}

// sortPaths pre-splits every sort field, once per query.
func (q Query) sortPaths() [][]string {
	paths := make([][]string, len(q.Sort))
	for i, k := range q.Sort {
		paths[i] = engine.SplitPath(k.Field)
	}
	return paths
}

// String renders a query compactly for trace lines.
func (q Query) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "filter=%s", compactJSON(q.Filter))
	if len(q.Projection) > 0 {
		fmt.Fprintf(&b, " projection=%s", compactJSON(q.Projection))
	}
	if len(q.Sort) > 0 {
		b.WriteString(" sort=")
		for i, k := range q.Sort {
			if i > 0 {
				b.WriteByte(',')
			}
			dir := "asc"
			if k.Desc {
				dir = "desc"
			}
			fmt.Fprintf(&b, "%s:%s", k.Field, dir)
		}
	}
	if q.Skip > 0 {
		fmt.Fprintf(&b, " skip=%d", q.Skip)
	}
	if q.Limit > 0 {
		fmt.Fprintf(&b, " limit=%d", q.Limit)
	}
	return b.String()
}
