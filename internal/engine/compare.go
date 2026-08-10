package engine

// compare.go defines a total order over every value a document can hold.
//
// Both filtering and sorting need one, because nothing stops `age` from being a
// number in one document and a string in the next. One function decides, so the
// two can never disagree.

import (
	"sort"
	"strings"
)

// absentValue is the sentinel for "this path does not exist in this document".
//
// It has to be distinguishable from JSON null: {"a": null} and {} are different
// documents, so $exists and {"$eq": null} must behave differently. An empty
// struct{} costs zero bytes.
type absentValue struct{}

// absent is the single instance of that sentinel. Declaring it as `any`
// (Go's alias for interface{}) means it can be returned wherever a document
// value is expected.
//
// Callers outside this package can never construct one, so it can never be
// confused with real data.
var absent any = absentValue{}

// isAbsent reports whether v is the "path not found" sentinel.
func isAbsent(v any) bool {
	_, ok := v.(absentValue)
	return ok
}

// Type ranks, lowest first. Values of different types are ordered by rank,
// which is what makes the order *total* — every pair of values compares.
//
//	absent < null < numbers < strings < booleans < arrays < objects
const (
	rankAbsent = iota
	rankNull
	rankNumber
	rankString
	rankBool
	rankArray
	rankObject
	rankUnknown // anything we do not recognise sorts last
)

// typeRank classifies a value.
//
// The `switch v.(type)` form is a type switch: Go's way of asking "what is
// actually inside this interface value?". Documents come from encoding/json so
// in practice the types are float64, string, bool, nil, []any, map[string]any —
// but filters are written by hand in Go, so int and friends show up too.
func typeRank(v any) int {
	switch v.(type) {
	case absentValue:
		return rankAbsent
	case nil:
		return rankNull
	case float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return rankNumber
	case string:
		return rankString
	case bool:
		return rankBool
	case []any:
		return rankArray
	case map[string]any:
		return rankObject
	default:
		return rankUnknown
	}
}

// toFloat converts any numeric value to float64.
//
// Documents always hold float64 (JSON has no integer type), but a filter
// written in Go can legitimately contain an int, so the comparison path has to
// accept both. The second return value is false for non-numbers.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// compare returns -1 if a sorts before b, +1 if after, and 0 if they are equal.
//
// Values of different types are ordered by type rank, so the result is a total
// order: sorting a mixed-type field is deterministic, and documents missing the
// sort field group together at the start.
//
// Note that comparison *operators* ($gt and friends) deliberately do not use
// the cross-type part of this order — see cmpNode in match.go. Sorting does.
func compare(a, b any) int {
	ra, rb := typeRank(a), typeRank(b)
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}

	switch ra {
	case rankAbsent, rankNull:
		return 0 // all absents are equal; all nulls are equal

	case rankNumber:
		fa, _ := toFloat(a)
		fb, _ := toFloat(b)
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		default:
			return 0
		}

	case rankString:
		return strings.Compare(a.(string), b.(string))

	case rankBool:
		ba, bb := a.(bool), b.(bool)
		switch {
		case ba == bb:
			return 0
		case !ba:
			return -1 // false < true
		default:
			return 1
		}

	case rankArray:
		// Element by element, then by length — the same rule strings follow.
		xs, ys := a.([]any), b.([]any)
		n := len(xs)
		if len(ys) < n {
			n = len(ys)
		}
		for i := 0; i < n; i++ {
			if c := compare(xs[i], ys[i]); c != 0 {
				return c
			}
		}
		switch {
		case len(xs) < len(ys):
			return -1
		case len(xs) > len(ys):
			return 1
		default:
			return 0
		}

	case rankObject:
		// Objects have no inherent order, so compare their keys in sorted
		// order. Arbitrary, but stable and deterministic, which is all sorting
		// needs — and it makes equality work out to deep equality.
		xs, ys := a.(map[string]any), b.(map[string]any)
		ka, kb := sortedKeys(xs), sortedKeys(ys)
		n := len(ka)
		if len(kb) < n {
			n = len(kb)
		}
		for i := 0; i < n; i++ {
			if c := strings.Compare(ka[i], kb[i]); c != 0 {
				return c
			}
			if c := compare(xs[ka[i]], ys[kb[i]]); c != 0 {
				return c
			}
		}
		switch {
		case len(ka) < len(kb):
			return -1
		case len(ka) > len(kb):
			return 1
		default:
			return 0
		}

	default:
		return 0
	}
}

// equalValues is deep equality, defined as "compares equal". Used by $eq, $ne,
// $in and $nin, so arrays and objects compare by content rather than identity.
func equalValues(a, b any) bool { return compare(a, b) == 0 }

// sortedKeys returns a map's keys in sorted order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
