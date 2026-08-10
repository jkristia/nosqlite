package engine

// match.go is the runtime half of the query engine: the tree of nodes that a
// filter document compiles into, and the path walking that feeds them.
//
// A filter is parsed exactly once, before any document is looked at (see
// compile.go). Then every document in the scan is handed to the same tree.

import "strconv"

// Matcher is one node in a compiled filter.
//
// In Go an interface is a set of method signatures; any type with those methods
// satisfies it automatically, with no "implements" keyword. So andNode,
// cmpNode and the rest are all Matchers simply by having a Match method.
//
// Why a tree rather than a closure or a switch inside the scan loop: a tree is
// *inspectable*. Adding secondary indexes later means writing a planner that
// walks this tree looking for a cmpNode on an indexed field, replacing that
// subtree with an index lookup and running the rest as a residual filter. With
// a closure there would be nothing to inspect. This is the single most
// important structural decision in the design, and it costs nothing today.
type Matcher interface {
	Match(doc map[string]any) bool
}

// ---------------------------------------------------------------------------
// Logical nodes
// ---------------------------------------------------------------------------

// matchAll matches every document. It is what an empty filter compiles to.
type matchAll struct{}

func (matchAll) Match(map[string]any) bool { return true }

// andNode matches when every child matches. Multiple keys in one filter object
// compile to this, as does an explicit $and.
type andNode struct{ children []Matcher }

func (n andNode) Match(doc map[string]any) bool {
	for _, child := range n.children {
		if !child.Match(doc) {
			return false // short-circuit: no need to evaluate the rest
		}
	}
	return true
}

// orNode matches when at least one child matches ($or).
type orNode struct{ children []Matcher }

func (n orNode) Match(doc map[string]any) bool {
	for _, child := range n.children {
		if child.Match(doc) {
			return true
		}
	}
	return false
}

// notNode inverts its child ($not).
type notNode struct{ child Matcher }

func (n notNode) Match(doc map[string]any) bool { return !n.child.Match(doc) }

// ---------------------------------------------------------------------------
// Comparison nodes
// ---------------------------------------------------------------------------

// cmpOp identifies which comparison a cmpNode performs.
type cmpOp int

const (
	opEq cmpOp = iota
	opNe
	opGt
	opGte
	opLt
	opLte
)

// cmpNode compares the value at a dotted path against a constant.
//
// The path is pre-split at compile time ("address.city" -> ["address","city"])
// so the scan never does string splitting per document.
type cmpNode struct {
	path  []string
	op    cmpOp
	value any
}

func (n cmpNode) Match(doc map[string]any) bool {
	// $ne is defined as "no candidate value equals this", so it is the negation
	// of $eq over the whole candidate set rather than a per-value test.
	if n.op == opNe {
		eq := cmpNode{path: n.path, op: opEq, value: n.value}
		return !eq.Match(doc)
	}

	found := lookupPath(doc, n.path)
	for _, v := range found {
		if n.matchValue(v) {
			return true
		}
		// Mongo array semantics: if the value at the path is an array, a scalar
		// comparison matches when ANY element matches. (Matching the array as a
		// whole was already tried on the line above, which is what makes
		// {"tags": ["math"]} match a document whose tags really is ["math"].)
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				if n.matchValue(el) {
					return true
				}
			}
		}
	}
	return false
}

// matchValue applies the operator to one concrete value.
func (n cmpNode) matchValue(v any) bool {
	switch n.op {
	case opEq:
		return equalValues(v, n.value)
	default:
		// Ordering comparisons only match WITHIN a type: {"age":{"$gte":30}}
		// does not match {"age":"36"}. This mirrors Mongo, and the alternative
		// is inventing coercion rules that will surprise someone.
		if typeRank(v) != typeRank(n.value) {
			return false
		}
		c := compare(v, n.value)
		switch n.op {
		case opGt:
			return c > 0
		case opGte:
			return c >= 0
		case opLt:
			return c < 0
		case opLte:
			return c <= 0
		}
		return false
	}
}

// inNode implements $in and $nin.
type inNode struct {
	path   []string
	values []any
	negate bool // true for $nin
}

func (n inNode) Match(doc map[string]any) bool {
	hit := false
	found := lookupPath(doc, n.path)

outer:
	for _, v := range found {
		for _, want := range n.values {
			if equalValues(v, want) {
				hit = true
				break outer
			}
		}
		// Same array rule as cmpNode: any element counts.
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				for _, want := range n.values {
					if equalValues(el, want) {
						hit = true
						break outer
					}
				}
			}
		}
	}
	if n.negate {
		return !hit
	}
	return hit
}

// existsNode implements $exists: does the path resolve to anything at all?
type existsNode struct {
	path []string
	want bool
}

func (n existsNode) Match(doc map[string]any) bool {
	found := lookupPath(doc, n.path)
	// lookupPath returns exactly one absent sentinel when nothing was found.
	exists := !(len(found) == 1 && isAbsent(found[0]))
	return exists == n.want
}

// ---------------------------------------------------------------------------
// Path walking
// ---------------------------------------------------------------------------

// lookupPath resolves a dotted path within a document and returns every value
// it reaches. When nothing is reached it returns a single `absent` sentinel, so
// callers never have to special-case an empty slice.
//
// It can return more than one value because of Mongo's implicit array
// traversal: for a document
//
//	{"items": [{"qty": 1}, {"qty": 5}]}
//
// the path "items.qty" reaches both 1 and 5, and a filter matches if any of
// them matches.
func lookupPath(doc map[string]any, path []string) []any {
	// The common case is a top-level scalar field, so handle it without
	// allocating a slice at all.
	if len(path) == 1 {
		if v, ok := doc[path[0]]; ok {
			return []any{v}
		}
		return []any{absent}
	}
	var out []any
	collectPath(doc, path, &out)
	if len(out) == 0 {
		return []any{absent}
	}
	return out
}

// collectPath is the recursive worker behind lookupPath. `out` is a pointer to
// a slice so each recursive call can append to the same result.
func collectPath(v any, path []string, out *[]any) {
	if len(path) == 0 {
		*out = append(*out, v)
		return
	}
	key := path[0]
	rest := path[1:]

	switch node := v.(type) {
	case map[string]any:
		if child, ok := node[key]; ok {
			collectPath(child, rest, out)
		}
	case []any:
		// A numeric segment indexes into the array: "items.0.qty".
		if i, err := strconv.Atoi(key); err == nil && i >= 0 && i < len(node) {
			collectPath(node[i], rest, out)
		}
		// And the same segment is also applied to each element, which is how
		// "items.qty" reaches into an array of objects.
		for _, el := range node {
			if m, ok := el.(map[string]any); ok {
				if child, ok := m[key]; ok {
					collectPath(child, rest, out)
				}
			}
		}
	}
	// Anything else (a scalar with path left to walk) simply contributes
	// nothing, which is what "absent" means.
}

// LookupOne resolves a path to a single value, for sorting. When a path reaches
// several values the first one wins; when it reaches none the result is the
// absent sentinel, which sorts before everything else.
//
// This is exported because the scan (in the parent package) extracts sort keys
// from each matching document before copying it. Everything else about path
// walking stays private to this package.
func LookupOne(doc map[string]any, path []string) any {
	found := lookupPath(doc, path)
	return found[0]
}
