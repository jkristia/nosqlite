// Package main builds the C shared library that non-Go languages load.
//
//	go build -buildmode=c-shared -o libnosqlite.so ./capi
//
// That produces libnosqlite.so (or .dylib / .dll) plus a generated
// libnosqlite.h. Python loads it with ctypes and TypeScript with koffi — the
// very same file, and the same JSON convention on both sides.
//
// Three rules make this boundary boring, which is what you want from an FFI
// boundary:
//
//  1. JSON in, JSON out. Every argument is a C string (JSON, or a plain string
//     for paths and collection names) and every return value is a C string of
//     JSON. One marshalling convention, nothing to keep in sync as the API
//     grows.
//
//  2. Never panic across the boundary. A Go panic unwinding into C is undefined
//     behaviour, so every exported function recovers and converts anything at
//     all — including a panic — into {"error": "..."}.
//
//  3. Handles, not pointers. cgo forbids storing Go pointers in C memory, so C
//     gets opaque int64 handles into a table on the Go side. That also turns
//     use-after-close into a clean error instead of a segfault.
//
// The caller must call nsq_free on every returned string: those are C heap
// allocations that the Go garbage collector knows nothing about. The Python
// wrapper hides this completely with a try/finally.
package main

// The comment block immediately above `import "C"` is real C code compiled into
// the library. We only need malloc/free's declarations here.

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/jkristia/nosqlite"
)

// main is required for a package main, but is never called in c-shared mode:
// the library has no entry point of its own.
func main() {}

// ---------------------------------------------------------------------------
// Handle table
// ---------------------------------------------------------------------------

// handle wraps one open database with a reference count.
//
// The refcount exists for one specific race that only the FFI boundary creates:
// if one thread calls nsq_close(h) while another is inside nsq_find(h, ...),
// closing the database would pull the file out from under an in-flight read.
// Python's ctypes releases the GIL around a foreign call, so two Python threads
// really can be inside Go at the same time — this is not theoretical.
//
// Every call acquires the handle (refs++), nsq_close marks it closed and waits
// for refs to reach zero before closing the database.
type handle struct {
	db     *nosqlite.DB
	refs   int
	closed bool
}

var (
	// registryMu guards everything below. There is deliberately no other
	// package-level mutable state in this file: the C ABI has to be a
	// thread-safe, reentrant surface from day one.
	registryMu sync.Mutex
	// registryCond is signalled when a refcount drops, so a waiting nsq_close
	// wakes up.
	registryCond = sync.NewCond(&registryMu)

	registry       = make(map[int64]*handle)
	nextID   int64 = 1
)

// acquire looks up a handle and takes a reference on it.
func acquire(id int64) (*handle, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	h, ok := registry[id]
	if !ok {
		return nil, fmt.Errorf("invalid database handle %d", id)
	}
	if h.closed {
		return nil, fmt.Errorf("database handle %d is closed", id)
	}
	h.refs++
	return h, nil
}

// release drops a reference and wakes anyone waiting to close.
func release(h *handle) {
	registryMu.Lock()
	defer registryMu.Unlock()
	h.refs--
	if h.refs == 0 {
		registryCond.Broadcast()
	}
}

// ---------------------------------------------------------------------------
// Response helpers
// ---------------------------------------------------------------------------

// respond marshals a value to JSON and hands C a freshly allocated string.
//
// C.CString copies into the C heap, which the Go GC does not manage — hence the
// nsq_free contract.
func respond(v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		// Fall back to a hand-built error object; this must never fail.
		return C.CString(`{"error":"nosqlite: could not encode response"}`)
	}
	return C.CString(string(b))
}

func respondError(err error) *C.char {
	return respond(map[string]any{"error": err.Error()})
}

// guard converts a panic into an error response. It is installed with `defer`
// at the top of every exported function; `recover()` only does anything inside
// a deferred call, which is why it has this shape.
//
// out is a pointer to the function's named return value, so the deferred
// closure can replace what gets returned.
func guard(out **C.char) {
	if r := recover(); r != nil {
		*out = respondError(fmt.Errorf("nosqlite: panic: %v", r))
	}
}

// goStr converts a possibly-NULL C string to a Go string.
func goStr(s *C.char) string {
	if s == nil {
		return ""
	}
	return C.GoString(s)
}

// ---------------------------------------------------------------------------
// Exports
//
// The //export comment is what makes cgo emit a C-callable symbol. It must sit
// immediately above the function, and the name must match exactly.
// ---------------------------------------------------------------------------

// openOptions is the optional JSON blob nsq_open accepts.
type openOptions struct {
	Sync      string `json:"sync"`       // "always" (default) | "never"
	Trace     string `json:"trace"`      // "off" | "writes" | "all" | "verbose"
	TraceFile string `json:"trace_file"` // default <dbpath>.trace
	FastOpen  bool   `json:"fast_open"`
}

//export nsq_open
func nsq_open(path *C.char, optsJSON *C.char) (out *C.char) {
	defer guard(&out)

	var opts openOptions
	if raw := goStr(optsJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &opts); err != nil {
			return respondError(fmt.Errorf("nosqlite: bad options JSON: %w", err))
		}
	}

	var goOpts []nosqlite.Option
	switch opts.Sync {
	case "", "always":
	case "never":
		goOpts = append(goOpts, nosqlite.WithSync(nosqlite.SyncNever))
	default:
		return respondError(fmt.Errorf("nosqlite: unknown sync mode %q", opts.Sync))
	}
	switch opts.Trace {
	case "", "off":
	case "writes":
		goOpts = append(goOpts, nosqlite.WithTrace(nosqlite.TraceWrites))
	case "all":
		goOpts = append(goOpts, nosqlite.WithTrace(nosqlite.TraceAll))
	case "verbose":
		goOpts = append(goOpts, nosqlite.WithTrace(nosqlite.TraceVerbose))
	default:
		return respondError(fmt.Errorf("nosqlite: unknown trace level %q", opts.Trace))
	}
	if opts.TraceFile != "" {
		goOpts = append(goOpts, nosqlite.WithTraceFile(opts.TraceFile))
	}
	if opts.FastOpen {
		goOpts = append(goOpts, nosqlite.WithFastOpen())
	}

	db, err := nosqlite.Open(goStr(path), goOpts...)
	if err != nil {
		return respondError(err)
	}

	registryMu.Lock()
	id := nextID
	nextID++
	registry[id] = &handle{db: db}
	registryMu.Unlock()

	return respond(map[string]any{"handle": id})
}

//export nsq_close
func nsq_close(id C.longlong) (out *C.char) {
	defer guard(&out)

	registryMu.Lock()
	h, ok := registry[int64(id)]
	if !ok {
		registryMu.Unlock()
		return respondError(fmt.Errorf("invalid database handle %d", int64(id)))
	}
	if h.closed {
		registryMu.Unlock()
		return respond(map[string]any{"ok": true})
	}
	// Mark closed first: any call arriving from now on fails cleanly rather
	// than joining the queue.
	h.closed = true
	// Then wait for calls already inside the database to finish. Cond.Wait
	// releases the mutex while it waits and re-acquires it on wake-up.
	for h.refs > 0 {
		registryCond.Wait()
	}
	delete(registry, int64(id))
	registryMu.Unlock()

	if err := h.db.Close(); err != nil {
		return respondError(err)
	}
	return respond(map[string]any{"ok": true})
}

//export nsq_insert
func nsq_insert(id C.longlong, coll *C.char, docJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	docID, err := c.InsertJSON([]byte(goStr(docJSON)))
	if err != nil {
		return respondError(err)
	}
	return respond(map[string]any{"id": docID})
}

//export nsq_insert_many
func nsq_insert_many(id C.longlong, coll *C.char, docsJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	var docs []map[string]any
	if err := json.Unmarshal([]byte(goStr(docsJSON)), &docs); err != nil {
		return respondError(fmt.Errorf("nosqlite: bad documents JSON: %w", err))
	}
	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	ids, err := c.InsertMany(docs)
	if err != nil {
		// Report the ids that did land alongside the error, so the caller knows
		// how far a partially written batch got.
		return respond(map[string]any{"error": err.Error(), "ids": ids})
	}
	return respond(map[string]any{"ids": ids})
}

//export nsq_replace
func nsq_replace(id C.longlong, coll *C.char, filterJSON *C.char, docJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	// Two JSON arguments rather than one bundled object, mirroring the Go
	// signature Replace(filter, doc). nsq_find bundles because a query has four
	// parts that keep growing; a replace has exactly these two.
	filter, err := decodeFilter(filterJSON)
	if err != nil {
		return respondError(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(goStr(docJSON)), &doc); err != nil {
		return respondError(fmt.Errorf("nosqlite: bad document JSON: %w", err))
	}

	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	n, err := c.Replace(filter, doc)
	if err != nil {
		return respondError(err)
	}
	return respond(map[string]any{"replaced": n})
}

//export nsq_delete
func nsq_delete(id C.longlong, coll *C.char, filterJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	filter, err := decodeFilter(filterJSON)
	if err != nil {
		return respondError(err)
	}
	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	n, err := c.Delete(filter)
	if err != nil {
		return respondError(err)
	}
	return respond(map[string]any{"deleted": n})
}

//export nsq_delete_many
func nsq_delete_many(id C.longlong, coll *C.char, filterJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	filter, err := decodeFilter(filterJSON)
	if err != nil {
		return respondError(err)
	}
	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	n, err := c.DeleteMany(filter)
	if err != nil {
		// Report the count that did land alongside the error, as nsq_insert_many
		// reports its ids. DeleteMany is not atomic: a crash mid-batch leaves a
		// prefix of the tombstones on disk, and n is how many actually landed —
		// the number to trust when the call failed.
		return respond(map[string]any{"error": err.Error(), "deleted": n})
	}
	return respond(map[string]any{"deleted": n})
}

// decodeFilter parses an optional filter argument. An empty or NULL string is a
// nil filter, which matches everything — the same convention nsq_count and
// nsq_replace already use.
func decodeFilter(filterJSON *C.char) (map[string]any, error) {
	raw := goStr(filterJSON)
	if raw == "" {
		return nil, nil
	}
	var filter map[string]any
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return nil, fmt.Errorf("nosqlite: bad filter JSON: %w", err)
	}
	return filter, nil
}

// wireQuery is the JSON shape of a query across the boundary. It mirrors
// nosqlite.Query but spells the sort keys out as objects, which is friendlier
// for every language on the other side.
//
// This shape is the bindings' contract, so it is written down for them in
// docs/bindings.md — keep the two in step.
type wireQuery struct {
	Filter map[string]any `json:"filter"`
	// Projection crosses as the document the caller wrote — {"name": 1} — and
	// is compiled on this side, so every binding gets one grammar and one set
	// of error messages rather than three re-implementations of the rules.
	Projection map[string]any `json:"projection"`
	Sort       []wireSortKey  `json:"sort"`
	Skip       int            `json:"skip"`
	Limit      int            `json:"limit"`
}

type wireSortKey struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc"`
}

func (w wireQuery) toQuery() nosqlite.Query {
	keys := make([]nosqlite.SortKey, len(w.Sort))
	for i, k := range w.Sort {
		keys[i] = nosqlite.SortKey{Field: k.Field, Desc: k.Desc}
	}
	return nosqlite.Query{
		Filter:     w.Filter,
		Projection: w.Projection,
		Sort:       keys,
		Skip:       w.Skip,
		Limit:      w.Limit,
	}
}

//export nsq_find
func nsq_find(id C.longlong, coll *C.char, queryJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	var wq wireQuery
	if raw := goStr(queryJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &wq); err != nil {
			return respondError(fmt.Errorf("nosqlite: bad query JSON: %w", err))
		}
	}
	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	docs, err := c.Find(wq.toQuery())
	if err != nil {
		return respondError(err)
	}
	if docs == nil {
		// Marshal a nil slice as [] rather than null, so callers can always
		// iterate the result.
		docs = []map[string]any{}
	}
	// NOTE: this materialises the whole result as one C string, a second full
	// copy on top of the Go slice. An unlimited find over a large collection is
	// the one way to blow up a process that is otherwise tiny. The bindings
	// apply a default limit; the real fix is a batched nsq_find_batch.
	return respond(map[string]any{"docs": docs})
}

//export nsq_count
func nsq_count(id C.longlong, coll *C.char, filterJSON *C.char) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	filter, err := decodeFilter(filterJSON)
	if err != nil {
		return respondError(err)
	}
	c, err := h.db.Collection(goStr(coll))
	if err != nil {
		return respondError(err)
	}
	n, err := c.Count(filter)
	if err != nil {
		return respondError(err)
	}
	return respond(map[string]any{"count": n})
}

//export nsq_collections
func nsq_collections(id C.longlong) (out *C.char) {
	defer guard(&out)

	h, err := acquire(int64(id))
	if err != nil {
		return respondError(err)
	}
	defer release(h)

	names := h.db.Collections()
	if names == nil {
		names = []string{}
	}
	return respond(map[string]any{"names": names})
}

//export nsq_free
func nsq_free(s *C.char) {
	if s != nil {
		// unsafe.Pointer is how Go hands a raw address to C's free(). This is
		// the counterpart of every C.CString above.
		C.free(unsafe.Pointer(s))
	}
}
