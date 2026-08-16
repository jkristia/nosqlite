package engine

// compile.go is the compile-time half of the query engine: turning a filter
// document written in the Mongo dialect into the Matcher tree from match.go.
//
// Compilation happens once per query, before any document is read. A filter
// error is therefore reported immediately, not halfway through a scan.

import (
	"fmt"
	"regexp"
	"strings"
)

// v1 operator set. Anything outside it is a compile error rather than a silent
// no-match: a typo'd $gte quietly returning zero rows is the worst possible
// behaviour.
const (
	opAnd    = "$and"
	opOr     = "$or"
	opNot    = "$not"
	opEqS    = "$eq"
	opNeS    = "$ne"
	opGtS    = "$gt"
	opGteS   = "$gte"
	opLtS    = "$lt"
	opLteS   = "$lte"
	opInS    = "$in"
	opNinS   = "$nin"
	opExists = "$exists"
	opRegexS = "$regex"
)

// CompileFilter turns a filter document into a Matcher tree.
//
// It is exported because it is useful on its own — for validating a filter
// before running it, and eventually as the input to an index planner.
func CompileFilter(filter map[string]any) (Matcher, error) {
	if len(filter) == 0 {
		return matchAll{}, nil
	}
	return compileDocument(filter)
}

// compileDocument compiles one filter object: every key is either a logical
// operator ($and/$or/$not) or a field path.
func compileDocument(filter map[string]any) (Matcher, error) {
	// Iterate keys in sorted order so the resulting tree — and therefore any
	// error message — is deterministic. Go map iteration order is randomised.
	nodes := make([]Matcher, 0, len(filter))
	for _, key := range sortedKeys(filter) {
		value := filter[key]

		if strings.HasPrefix(key, "$") {
			node, err := compileLogical(key, value)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, node)
			continue
		}

		node, err := compileField(key, value)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}

	if len(nodes) == 1 {
		return nodes[0], nil
	}
	// Several keys in one object are implicitly ANDed.
	return andNode{children: nodes}, nil
}

// compileLogical handles a top-level $and / $or / $not.
func compileLogical(op string, value any) (Matcher, error) {
	switch op {
	case opAnd, opOr:
		branches, err := compileBranches(op, value)
		if err != nil {
			return nil, err
		}
		if op == opAnd {
			return andNode{children: branches}, nil
		}
		return orNode{children: branches}, nil

	case opNot:
		sub, ok := asDocument(value)
		if !ok {
			return nil, fmt.Errorf("nosqlite: %s expects a filter document, got %T", op, value)
		}
		child, err := compileDocument(sub)
		if err != nil {
			return nil, err
		}
		return notNode{child: child}, nil

	default:
		return nil, fmt.Errorf("nosqlite: unknown top-level operator %q", op)
	}
}

// compileBranches compiles the array argument of $and / $or.
func compileBranches(op string, value any) ([]Matcher, error) {
	// A filter that arrived as JSON decodes arrays to []any; a filter written
	// in Go might use []map[string]any. Accept both.
	var raw []any
	switch v := value.(type) {
	case []any:
		raw = v
	case []map[string]any:
		for _, m := range v {
			raw = append(raw, m)
		}
	default:
		return nil, fmt.Errorf("nosqlite: %s expects an array of filter documents, got %T", op, value)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("nosqlite: %s expects a non-empty array", op)
	}

	branches := make([]Matcher, 0, len(raw))
	for i, item := range raw {
		sub, ok := asDocument(item)
		if !ok {
			return nil, fmt.Errorf("nosqlite: %s[%d] must be a filter document, got %T", op, i, item)
		}
		child, err := compileDocument(sub)
		if err != nil {
			return nil, err
		}
		branches = append(branches, child)
	}
	return branches, nil
}

// compileField compiles one "field: constraint" pair.
//
// The constraint is either an operator document ({"$gte": 30}) or a bare
// literal, which is shorthand for $eq.
func compileField(field string, value any) (Matcher, error) {
	if field == "" {
		return nil, fmt.Errorf("nosqlite: empty field name in filter")
	}
	path := SplitPath(field)

	sub, isDoc := asDocument(value)
	if !isDoc || !allKeysAreOperators(sub) {
		if isDoc && hasAnyOperatorKey(sub) {
			// {"a": {"$gt": 1, "b": 2}} — half operators, half literal keys.
			// Mongo rejects this too, and guessing would be worse.
			return nil, fmt.Errorf("nosqlite: field %q mixes operators and plain keys", field)
		}
		// Bare literal: {"name": "Ada"} means {"name": {"$eq": "Ada"}}.
		return cmpNode{path: path, op: opEq, value: value}, nil
	}

	nodes := make([]Matcher, 0, len(sub))
	for _, op := range sortedKeys(sub) {
		node, err := compileOperator(field, path, op, sub[op])
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	return andNode{children: nodes}, nil
}

// compileOperator compiles one $operator applied to one field.
func compileOperator(field string, path []string, op string, arg any) (Matcher, error) {
	switch op {
	case opEqS:
		return cmpNode{path: path, op: opEq, value: arg}, nil
	case opNeS:
		return cmpNode{path: path, op: opNe, value: arg}, nil
	case opGtS:
		return cmpNode{path: path, op: opGt, value: arg}, nil
	case opGteS:
		return cmpNode{path: path, op: opGte, value: arg}, nil
	case opLtS:
		return cmpNode{path: path, op: opLt, value: arg}, nil
	case opLteS:
		return cmpNode{path: path, op: opLte, value: arg}, nil

	case opInS, opNinS:
		values, err := asArray(arg)
		if err != nil {
			return nil, fmt.Errorf("nosqlite: %s on field %q: %w", op, field, err)
		}
		return inNode{path: path, values: values, negate: op == opNinS}, nil

	case opExists:
		want, ok := arg.(bool)
		if !ok {
			return nil, fmt.Errorf("nosqlite: %s on field %q expects true or false, got %T", op, field, arg)
		}
		return existsNode{path: path, want: want}, nil

	case opRegexS:
		pattern, ok := arg.(string)
		if !ok {
			return nil, fmt.Errorf("nosqlite: %s on field %q expects a string pattern, got %T", op, field, arg)
		}
		// Go's RE2 syntax: unanchored by default, so a plain literal pattern
		// (no special characters) behaves as a substring search — the common
		// case — while inline flags like "(?i)" still give full regex power
		// when needed.
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("nosqlite: %s on field %q: invalid pattern: %w", op, field, err)
		}
		return regexNode{path: path, re: re}, nil

	case opNot:
		sub, ok := asDocument(arg)
		if !ok || !allKeysAreOperators(sub) {
			return nil, fmt.Errorf("nosqlite: %s on field %q expects an operator document", op, field)
		}
		inner, err := compileField(field, sub)
		if err != nil {
			return nil, err
		}
		return notNode{child: inner}, nil

	default:
		return nil, fmt.Errorf("nosqlite: unknown operator %q on field %q", op, field)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

// SplitPath turns "address.city" into ["address","city"] once, at compile time.
//
// Exported because the parent package pre-splits its sort fields the same way,
// and both halves must agree on what a dotted path means.
func SplitPath(field string) []string { return strings.Split(field, ".") }

// asDocument accepts the several Go shapes a nested filter object can arrive
// in. JSON decoding always produces map[string]any; hand-written Go filters
// sometimes use map[string]string and similar, but supporting the two common
// ones is enough.
func asDocument(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	default:
		return nil, false
	}
}

// asArray normalises the argument of $in / $nin.
func asArray(v any) ([]any, error) {
	switch a := v.(type) {
	case []any:
		return a, nil
	case []string:
		out := make([]any, len(a))
		for i, s := range a {
			out[i] = s
		}
		return out, nil
	case []int:
		out := make([]any, len(a))
		for i, n := range a {
			out[i] = float64(n)
		}
		return out, nil
	case []float64:
		out := make([]any, len(a))
		for i, n := range a {
			out[i] = n
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expects an array, got %T", v)
	}
}

func allKeysAreOperators(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if !strings.HasPrefix(k, "$") {
			return false
		}
	}
	return true
}

func hasAnyOperatorKey(m map[string]any) bool {
	for k := range m {
		if strings.HasPrefix(k, "$") {
			return true
		}
	}
	return false
}
