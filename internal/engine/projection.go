package engine

// projection.go narrows a document on the way out.
//
// A projection is a document, in the same Mongo dialect the filter uses:
//
//	{"name": 1, "address.city": 1}   // inclusion: only these fields survive
//	{"email": 0, "address.zip": 0}   // exclusion: everything BUT these
//
// The two cannot be mixed in one projection — "keep name" and "drop email"
// together would leave every other field's fate unstated — with one exception:
// _id, which is included by default and so has to be excludable from an
// otherwise-inclusion projection. MongoDB draws the line in exactly the same
// place, and this package follows it.
//
// A dotted path rebuilds a partial subdocument rather than flattening the key:
// {"address.city": 1} yields {"address": {"city": "Oslo"}}, never
// {"address.city": "Oslo"}. That is what makes a projected document still a
// document — the same shape the caller stored, with fewer fields in it.
//
// Applying a projection replaces the deep copy the scan would have made
// anyway, so a narrower result is also strictly less copying. What it does NOT
// avoid is the json.Unmarshal that produced the document in the first place;
// see the partial-parsing item in docs/design.md §11.

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
	// (exclusion).
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

	var (
		root      = &projNode{}
		fields    int    // non-_id fields seen, which is what sets the direction
		include   bool   // the direction those fields agree on
		first     string // the field that set it, named in the error if one disagrees
		idPresent bool
		idKeep    bool
	)

	// Sorted so that a spec with several mistakes in it always reports the same
	// one, whatever order the map happened to iterate in.
	for _, field := range sortedKeys(spec) {
		keep, err := projectionValue(field, spec[field])
		if err != nil {
			return nil, err
		}

		// _id is exempt from the mixing rule below, in both directions: it is
		// the one field that is included by default, so naming it says which
		// way that default goes and nothing about the rest of the document.
		if field == "_id" {
			idPresent, idKeep = true, keep
			continue
		}

		if err := validPath(field); err != nil {
			return nil, err
		}
		if fields == 0 {
			include, first = keep, field
		} else if keep != include {
			kept, dropped := first, field
			if keep {
				kept, dropped = field, first
			}
			return nil, fmt.Errorf(
				"nosqlite: projection mixes inclusion and exclusion: %q says keep, %q says drop; "+
					"a projection either lists the fields to keep or the fields to drop, not both",
				kept, dropped)
		}
		fields++
		root.insert(SplitPath(field))
	}

	if fields == 0 {
		// Only _id was named, so it is the field that sets the direction after
		// all: {"_id": 1} is an inclusion projection of nothing else, {"_id": 0}
		// an exclusion of nothing else.
		root.insert([]string{"_id"})
		return &Projection{include: idKeep, root: root}, nil
	}

	// Otherwise _id rides along with the direction the other fields set. The
	// tree names what an inclusion projection KEEPS and what an exclusion
	// projection DROPS, so _id belongs in it either when it survives an
	// inclusion or when it is dropped from an exclusion.
	keepID := !idPresent || idKeep
	if include == keepID {
		root.insert([]string{"_id"})
	}
	return &Projection{include: include, root: root}, nil
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
