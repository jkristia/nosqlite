package engine

// sort.go orders query results, and keeps a sorted query with a limit from
// having to hold every match in memory.
//
// A "top 10 by age" query over a million matching documents should hold ten
// documents, not a million. The classic way to do that is a bounded heap: keep
// at most k entries, and whenever an eleventh arrives, throw away whichever of
// the eleven is currently worst.
//
// The subtlety is that to cheaply find the *worst* of the k you keep, the heap
// has to be ordered backwards from the sort the user asked for — a max-heap
// with respect to the sort order, so the element most likely to be evicted sits
// at the root.

import (
	"container/heap"
	"sort"
)

// SortKey is one level of an ordering.
//
// It lives down here with the code that acts on it, but it is part of the
// public API: the parent package re-exports it as nosqlite.SortKey.
type SortKey struct {
	Field string // dotted path, e.g. "address.city"
	Desc  bool   // false = ascending
}

// SortEntry is one candidate result: the document, plus its extracted sort key
// values so the comparison never re-walks the document's paths.
//
// The fields are exported because the scan builds these — it is the half that
// owns documents and file offsets, while this package only knows how to order
// them.
type SortEntry struct {
	Doc  map[string]any
	Keys []any
	Seq  int // position in the scan, used to break ties deterministically
}

// entryLess reports whether a should come before b under the given sort keys.
//
// Ties fall back to scan order, so two documents with the same age come out in
// insertion order. This matters more than it looks: a heap reorders whatever
// you put in it, so without an explicit tiebreak a "top 3" query would return
// tied documents in an arbitrary order that changed with the data.
func entryLess(keys []SortKey, a, b SortEntry) bool {
	for i := range keys {
		c := compare(a.Keys[i], b.Keys[i])
		if keys[i].Desc {
			c = -c
		}
		if c != 0 {
			return c < 0
		}
	}
	return a.Seq < b.Seq
}

// SortEntries orders a materialised result set in place. Used for a sort with
// no limit, where every match had to be kept anyway.
func SortEntries(entries []SortEntry, keys []SortKey) {
	// SliceStable keeps insertion order between entries that compare equal,
	// which makes results reproducible run to run.
	sort.SliceStable(entries, func(i, j int) bool {
		return entryLess(keys, entries[i], entries[j])
	})
}

// entryHeap implements container/heap's interface.
//
// container/heap is not a data structure — it is five methods you provide, and
// a set of free functions (heap.Push, heap.Pop, heap.Init) that operate on
// them. This is Go's usual answer to generics-before-generics: describe the
// behaviour, let the library drive it.
type entryHeap struct {
	entries []SortEntry
	keys    []SortKey
}

func (h entryHeap) Len() int { return len(h.entries) }

// Less is deliberately reversed: heap.Pop removes the element Less considers
// smallest, and we want it to remove the WORST entry under the user's sort. So
// "less" here means "sorts later in the user's order".
func (h entryHeap) Less(i, j int) bool {
	return entryLess(h.keys, h.entries[j], h.entries[i])
}

func (h entryHeap) Swap(i, j int) {
	h.entries[i], h.entries[j] = h.entries[j], h.entries[i]
}

// Push and Pop take/return `any` because they predate generics. They operate on
// the raw slice; heap.Push and heap.Pop wrap them with the sifting logic.
//
// Note the pointer receiver (*entryHeap): these two change the slice length, so
// they need to modify the struct itself rather than a copy.
func (h *entryHeap) Push(x any) {
	h.entries = append(h.entries, x.(SortEntry))
}

func (h *entryHeap) Pop() any {
	old := h.entries
	n := len(old)
	item := old[n-1]
	old[n-1] = SortEntry{} // let the GC reclaim the document we are dropping
	h.entries = old[:n-1]
	return item
}

// TopK collects the best k entries under a sort order using O(k) memory.
type TopK struct {
	h entryHeap
	k int
}

// NewTopK returns a collector that will keep at most k entries under keys.
func NewTopK(k int, keys []SortKey) *TopK {
	return &TopK{
		h: entryHeap{
			entries: make([]SortEntry, 0, k+1),
			keys:    keys,
		},
		k: k,
	}
}

// Add offers an entry to the collection. If the collection is full and the new
// entry is worse than everything already held, it is dropped immediately.
func (t *TopK) Add(e SortEntry) {
	if len(t.h.entries) < t.k {
		heap.Push(&t.h, e)
		return
	}
	// t.h.entries[0] is the worst entry currently held (see Less above). If the
	// newcomer is no better, there is nothing to do — and this is the common
	// case once the heap has filled up, so it is worth the early exit.
	if !entryLess(t.h.keys, e, t.h.entries[0]) {
		return
	}
	heap.Push(&t.h, e)
	heap.Pop(&t.h)
}

// Sorted returns the collected entries in the user's sort order.
//
// A heap is only partially ordered, so the final ordering is a real sort — but
// over at most k entries, not over every match.
func (t *TopK) Sorted() []SortEntry {
	out := t.h.entries
	sort.SliceStable(out, func(i, j int) bool {
		return entryLess(t.h.keys, out[i], out[j])
	})
	return out
}
