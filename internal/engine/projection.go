package engine

// projection.go narrows a document on the way out.
//
// A projection is a document, in the same Mongo dialect the filter uses:
//
//	{"name": 1, "address.city": 1}   // inclusion: only these fields survive
//	{"email": 0, "address.zip": 0}   // exclusion: everything BUT these
//
// Inclusion wins when a projection has both: name one field to include and the
// result is exactly the fields you named, so an exclusion beside it has nothing
// left to drop and is ignored. {"name": 1, "email": 0} returns name and _id.
//
// The one thing that is not ignored is a contradiction — an exclusion that
// names the same field as an inclusion, {"address": 1, "address.city": 0}. That
// is a real disagreement about a field the projection was going to return, so
// it is an error.
//
// _id needs no rule of its own: it is included by default, and {"_id": 0}
// excludes it like any other field.
//
// A dotted path rebuilds a partial subdocument rather than flattening the key:
// {"address.city": 1} yields {"address": {"city": "Oslo"}}, never
// {"address.city": "Oslo"}. That is what makes a projected document still a
// document — the same shape the caller stored, with fewer fields in it.
//
// Applying a projection replaces the deep copy the scan would have made
// anyway, so a narrower result is also strictly less copying. What it does NOT
// avoid is the json.Unmarshal that produced the document in the first place;
// see the partial-parsing item in docs/design.md §8.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Projection is a compiled projection document, ready to apply to each
// matching document. The nil *Projection is valid and means "the whole
// document", which is what lets the scan call Apply unconditionally.
type Projection struct {
	// include distinguishes the two kinds. Every path in the tree is then
	// either the only thing kept (inclusion) or the only thing dropped
	// (exclusion) — a compiled projection has one direction, whatever the
	// spec it came from named.
	include bool
	root    *projNode
}

// projNode is one segment of a projection path, in a tree so that
// {"address.city": 1, "address.zip": 1} walks "address" once.
//
// leaf means a path ended here, so the whole subtree below it is named. A leaf
// has no children: once {"address": 1} says "all of address", a longer
// {"address.city": 1} adds nothing, and the shorter path wins regardless of
// which order the two arrive in.
type projNode struct {
	leaf     bool
	children map[string]*projNode
}

func (n *projNode) insert(path []string) {
	cur := n
	for _, seg := range path {
		if cur.leaf {
			return // a shorter path already covers this one
		}
		if cur.children == nil {
			cur.children = make(map[string]*projNode)
		}
		next := cur.children[seg]
		if next == nil {
			next = &projNode{}
			cur.children[seg] = next
		}
		cur = next
	}
	cur.leaf = true
	cur.children = nil // this path now covers everything below it
}

// CompileProjection turns a projection document into a Projection. A nil or
// empty spec compiles to a nil *Projection, meaning no projection at all.
//
// It is exported for the same reason CompileFilter is: validating a projection
// without running a query is useful on its own.
func CompileProjection(spec map[string]any) (*Projection, error) {
	if len(spec) == 0 {
		return nil, nil
	}

	// One field's 1 or 0, kept with the path it compiled to so an error can
	// name the field the way the caller wrote it.
	type entry struct {
		field string
		path  []string
	}
	var incl, excl []entry

	// Sorted so that a spec with several mistakes in it always reports the same
	// one, whatever order the map happened to iterate in.
	for _, field := range sortedKeys(spec) {
		keep, err := projectionValue(field, spec[field])
		if err != nil {
			return nil, err
		}
		if err := validPath(field); err != nil {
			return nil, err
		}
		e := entry{field, SplitPath(field)}
		if keep {
			incl = append(incl, e)
		} else {
			excl = append(excl, e)
		}
	}

	root := &projNode{}

	// Nothing included: the spec is an exclusion list, and the tree names what
	// to drop. _id is in it only if the caller put it there.
	if len(incl) == 0 {
		for _, e := range excl {
			root.insert(e.path)
		}
		return &Projection{include: false, root: root}, nil
	}

	// One inclusion makes the whole projection an inclusion. The tree is the
	// included paths, and every field not in it is already gone — which is why
	// the exclusions below need only be checked, not applied.
	for _, e := range incl {
		root.insert(e.path)
	}

	idExcluded := false
	for _, e := range excl {
		for _, in := range incl {
			// Two paths where one contains the other name the same field, so
			// the projection both keeps it and drops it. Anything else is an
			// exclusion of a field the inclusion list had already dropped:
			// redundant, and harmless.
			if pathContains(e.path, in.path) || pathContains(in.path, e.path) {
				return nil, fmt.Errorf(
					"nosqlite: projection includes %q and excludes %q, which name the same field; "+
						"drop one of them — an inclusion already drops every field it does not name",
					in.field, e.field)
			}
		}
		if e.field == "_id" {
			idExcluded = true
		}
	}

	// _id is included by default, so it joins the tree unless it was named for
	// exclusion.
	if !idExcluded {
		root.insert([]string{"_id"})
	}
	return &Projection{include: true, root: root}, nil
}

// pathContains reports whether outer names a field that inner is part of —
// ["address"] contains ["address","city"], and every path contains itself.
func pathContains(outer, inner []string) bool {
	if len(outer) > len(inner) {
		return false
	}
	for i, seg := range outer {
		if inner[i] != seg {
			return false
		}
	}
	return true
}

// projectionValue reads one field's 1/0 (or true/false).
func projectionValue(field string, v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case float64:
		return t != 0, nil
	case int:
		return t != 0, nil
	case int64:
		return t != 0, nil
	case json.Number:
		return t.String() != "0", nil
	case map[string]any:
		// {"tags": {"$slice": 2}} and the positional {"tags.$": 1} are real
		// Mongo, and deliberately not implemented — say so, rather than
		// silently treating the operator document as truthy.
		return false, fmt.Errorf(
			"nosqlite: projection on %q takes 1 or 0, got an operator document; "+
				"array projection operators ($slice, positional $) are not supported", field)
	default:
		return false, fmt.Errorf("nosqlite: projection on %q takes 1 or 0, got %v", field, v)
	}
}

// validPath rejects the field names that cannot mean anything.
func validPath(field string) error {
	if field == "" {
		return fmt.Errorf("nosqlite: empty field name in projection")
	}
	for _, seg := range SplitPath(field) {
		if seg == "" {
			return fmt.Errorf("nosqlite: projection path %q has an empty segment", field)
		}
	}
	if strings.HasPrefix(field, "$") {
		return fmt.Errorf("nosqlite: projection field %q looks like an operator; "+
			"a projection names fields, e.g. {\"name\": 1}", field)
	}
	return nil
}

// Apply narrows doc and returns a fresh, independent document.
//
// It always copies, because the caller of a query owns its results and the
// scan's scratch map is about to be reused. That makes Apply a drop-in
// replacement for the deep copy the scan would otherwise make — a projection
// costs no extra pass, it just copies less.
//
// A nil *Projection copies the whole document, which is why the scan can call
// this without checking.
func (p *Projection) Apply(doc map[string]any) map[string]any {
	if p == nil {
		return copyMap(doc)
	}
	if p.include {
		return includeMap(doc, p.root)
	}
	return excludeMap(doc, p.root)
}

// includeMap keeps only what the tree names, so it walks the TREE and looks
// each name up in the document.
func includeMap(src map[string]any, node *projNode) map[string]any {
	out := make(map[string]any, len(node.children))
	for key, child := range node.children {
		v, present := src[key]
		if !present {
			continue // a projected field the document does not have stays absent
		}
		if child.leaf {
			out[key] = copyValue(v)
			continue
		}
		if sub, ok := includeValue(v, child); ok {
			out[key] = sub
		}
	}
	return out
}

// includeValue descends one level for a path with segments still to go. The
// bool is false when the value cannot be descended into at all.
func includeValue(v any, node *projNode) (any, bool) {
	switch t := v.(type) {
	case map[string]any:
		// An empty result is still a result: {"address.city": 1} against a
		// document whose address has no city yields {"address": {}}, which says
		// "the field is there, the projected part of it is not".
		return includeMap(t, node), true
	case []any:
		// An array of subdocuments is projected element-wise, so
		// {"items.qty": 1} yields one narrowed object per item. Elements that
		// are not documents cannot carry the path and drop out.
		out := make([]any, 0, len(t))
		for _, el := range t {
			if m, ok := el.(map[string]any); ok {
				out = append(out, includeMap(m, node))
			}
		}
		return out, true
	default:
		// A scalar with path left to walk contributes nothing, which is the
		// same thing lookupPath does when a filter walks off the end of one.
		return nil, false
	}
}

// excludeMap drops what the tree names, so it walks the DOCUMENT and consults
// the tree — the mirror image of includeMap.
func excludeMap(src map[string]any, node *projNode) map[string]any {
	out := make(map[string]any, len(src))
	for key, v := range src {
		child, named := node.children[key]
		if !named {
			out[key] = copyValue(v)
			continue
		}
		if child.leaf {
			continue // dropped outright
		}
		out[key] = excludeValue(v, child)
	}
	return out
}

func excludeValue(v any, node *projNode) any {
	switch t := v.(type) {
	case map[string]any:
		return excludeMap(t, node)
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			if m, ok := el.(map[string]any); ok {
				out[i] = excludeMap(m, node)
			} else {
				out[i] = copyValue(el)
			}
		}
		return out
	default:
		// Nothing to remove from a scalar the path runs past; keep it whole.
		return copyValue(v)
	}
}

// copyMap / copyValue are the projection's own deep copy, for the parts of a
// document it keeps wholesale. They mirror deepCopyMap / deepCopy in the parent
// package's scan.go, which handles the no-projection case; the two cannot be
// shared because engine may not import its parent.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = copyValue(v)
	}
	return out
}

func copyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return copyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			out[i] = copyValue(el)
		}
		return out
	default:
		// Scalars (string, float64, bool, nil) are immutable in Go and can be
		// shared as they are.
		return v
	}
}
