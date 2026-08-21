# Design

An embedded document store in Go, in the spirit of SQLite: no server, no daemon,
one file on disk, linked into the host process.

This is the high-level view — what it is for, what it costs, and where the parts
meet. The bytes are in [`file-format.md`](file-format.md), the write paths in
[`records.md`](records.md), the query dialect in [`filters.md`](filters.md).

Read [`nosql-primer.md`](nosql-primer.md) first if *collection* and *document*
aren't yet familiar.

---

## 1. Goals

- **Embedded.** No network, no separate process. `Open(path)` and you have a
  database.
- **One file.** Copy it, mail it, check it into a fixture directory.
- **Mongo-dialect filters**, so Go, Python and TypeScript share one query
  language with no translation layer.
- **Go standard library only.** No third-party dependencies.
- **Durable by default.** A completed `Insert` survives `kill -9`.
- **Small, flat memory footprint.** A million documents costs ~12 MB of index;
  the documents stay on disk and are streamed past, never all resident. Datasets
  much larger than RAM are fine.
- **Small enough to read in an afternoon.**

## Non-goals for v1

Deliberately, not accidentally — each has a named extension point in §8.

- Aggregation, field updates (`$set`), transactions, multi-document atomicity.
- Secondary indexes. All queries are full scans, which is the price of the
  memory target: queries are I/O- and parse-bound until indexes land.
- Compaction. Replace and delete exist; the space a superseded record holds is
  not reclaimed yet ([`compaction.md`](compaction.md)).
- Multi-process access, replication, networking.

---

## 2. Data model

```
./demo.nsq              the database. One file. Everything is in here.
./demo.nsq.trace        human-readable trace of every operation (optional)
```

- A **database** is one file. `Open("./demo.nsq")` creates it if missing.
- A **collection** is a named group of records within that file, created on first
  insert. Names are `[A-Za-z0-9_-]{1,64}` and are recorded by a catalog record,
  so a collection with no documents still exists after a restart.
- A **document** is any JSON object with a string `_id`, unique within its
  collection. If the caller doesn't supply one, it is generated: 8 bytes of
  big-endian Unix nanoseconds plus 8 random bytes, hex-encoded. Time-prefixed
  means ids sort by creation order; the random tail makes collisions a non-issue.

**A note on the word "log".** The database file *is* an append-only log
internally, and the trace file is also called a log in normal speech. These docs
say **database file** for the data and **trace file** for the debugging output.
The trace file is never read back by the database.

**Type mapping.** Documents are `map[string]any` decoded by `encoding/json`:

| JSON | Go | Python | TypeScript |
| --- | --- | --- | --- |
| object | `map[string]any` | `dict` | object |
| array | `[]any` | `list` | array |
| string | `string` | `str` | `string` |
| number | `float64` | `int` / `float` | `number` |
| bool | `bool` | `bool` | `boolean` |
| null | `nil` | `None` | `null` |

The `float64` row is the one to remember: **JSON has no integer type**, so `42`
round-trips through Go as `float64(42)`. Comparison and sorting treat all numbers
as one type. Python's `json.loads` hands back an `int` again and every JS number
is a double, so this is only visible from Go.

---

## 3. Memory model — an offset index, not a document cache

**Target: 1,000,000 documents in ~12 MB of process memory, with the documents
never all resident.**

Memory holds only what is needed to *find* a record, and the minimum for that is
where it starts and how long it is:

```go
type Collection struct {
    db   *DB
    name string
    id   uint16      // as written in each record header

    offsets []int64  // byte offset of each record's payload
    lengths []uint32 // payload length

    ids *idTable     // nil until something actually needs _id lookup
}
```

That is 12 bytes per document. Nothing else is retained.

**Why parallel arrays and not `[]struct` or a map**, in order of how much it
matters:

1. **The Go GC never scans them.** `[]int64` and `[]uint32` hold no pointers, so
   the collector treats each as one opaque block regardless of length. A
   `map[string]*entry` with a million entries is a million string headers plus a
   million pointers to walk on *every* cycle — sustained CPU burn and pause time.
   Pointer-free is the whole trick.
2. **Two allocations total**, not two million.
3. A `[]struct{int64; uint32}` would pad to 16 bytes; splitting saves 25% and
   keeps each array dense for sequential access.

**What is deliberately not in memory:**

- **Parsed documents.** No document cache, no LRU. A read parses from disk and
  the result is discarded unless it matched.
- **An `_id` → position map**, unless earned. The `idTable` is built lazily, on
  the first caller-supplied `_id`, by-id lookup, or mutation. Workloads that only
  use generated ids never pay for it. When built it is open-addressed and
  pointer-free: `fingerprints []uint64` plus `positions []uint32`, ≈24 bytes per
  document. A fingerprint match is verified by reading that one record, so there
  are no false positives, only a rare extra read.
- **A read cache.** The OS already has one. The page cache is shared across
  processes, evictable under pressure, and not counted against this heap;
  re-implementing it badly inside the database is the classic mistake.

### The budget, 1M documents averaging 300 B

| | Bytes/doc | 1M docs |
| --- | --- | --- |
| `offsets` | 8 | 8 MB |
| `lengths` | 4 | 4 MB |
| scan buffer (one, reused) | — | 64 KB |
| result set | — | O(`Limit`) |
| **steady state** | **12** | **≈ 12 MB** |
| `idTable`, if built | +24 | +24 MB |
| *(the file on disk)* | 312 | 312 MB |

That ceiling is ~85M documents per GB of index, with the data itself bounded only
by disk.

### Readers do not need the lock

This falls out of the log being append-only, and it shapes everything else:
**bytes already written are never modified, and index entries `[0, n)` are never
rewritten.** A reader takes the read lock just long enough to copy a snapshot —
`n`, the two slice headers, the file handle, the file size — and then runs its
entire scan with no lock held.

Reads go through `ReadAt` (`pread`), which does not touch the file's shared seek
offset, so it is safe concurrently with an appending writer on the very same
`*os.File`. No second handle, no reader lock, no writer starvation during a long
scan.

> **The rule this imposes:** index slices may be appended to and reallocated, but
> **element `i` must never be written after publication.** Anything that would
> violate it — a replace, a delete, in-place compaction — swaps in whole new
> slices under the write lock instead ([`records.md`](records.md)).

---

## 4. Query execution

Full scan, always, in v1 — but a *streaming* scan, so peak memory is a function
of the result size, not the collection size:

```
snapshot the index under RLock, release
for each of this collection's records, in offset order:
      read payload into the reused scratch buffer
      json.Unmarshal into a scratch map        ← the only per-document allocation
      Matcher.Match
      no match → clear(scratch) and continue   ← dropped, never retained
      match    → deep-copy into the result, narrowed by Query.Projection
apply skip / limit
```

**Bounding the result set.** Three cases, and only the last is unbounded:

| query shape | memory |
|---|---|
| no sort | O(`Limit`) — the scan stops once `Skip+Limit` have matched |
| sort + limit | O(`Skip+Limit`) — a bounded heap, so a top-10 over a million matches holds ten |
| sort, no limit | O(matches) — every match must be materialised before it can be ordered |

The last one is inherent to sorting rather than to this design: you cannot know
the first result until you have seen the last candidate. Adding a `Limit` is the
answer. An external merge sort would be the other one, and v1 does not have it.

`Find` returns a slice, so a filter matching a million documents produces a
million documents in the caller's memory no matter how frugal the engine was.
`ForEach` is the streaming escape hatch: it hands each match to a callback and
retains nothing. It is a callback rather than a cursor deliberately — a cursor
needs its own lifetime, a `Close`, and rules for outliving a write, whereas a
callback's scan is bounded by the call and needs no lock at all.

Results are **deep-copied** out of the scratch map, which is what makes reusing
that map safe and means a caller cannot corrupt anything by mutating a result.
**The projection is that copy**, not a pass over it, so a narrower result is
strictly less copying.

**What this costs.** Every query parses every document: roughly 1–3 seconds for a
full scan of a million 300-byte records, dominated by `encoding/json`. That is
the honest trade for a 12 MB footprint, and the same trade SQLite makes before
you add an index. Two things fix it later, both provisioned for: secondary
indexes, and partial parsing (§8).

---

## 5. Concurrency

Four properties that get confused with each other, and where each is actually
handled:

| property | means | where |
|---|---|---|
| **Atomicity** | a write is all-or-nothing; no half-documents | one `Write` of one framed record + CRC |
| **Isolation** | a reader never observes a write in progress | `RWMutex`; the writer holds it exclusively |
| **Durability** | a write that returned success survives a crash | `fsync` policy |
| **Linearizability** | all writes appear in one global order matching real time | falls out of the single append point |

Atomicity and durability are independent: `SyncNever` gives atomic-but-not-durable
writes — a crash may lose the last N inserts, but never leaves half of one on
disk.

**What v1 guarantees, in-process:**

- **One `sync.RWMutex` on the `DB`**, guarding the append point, the file size,
  the catalog and the index arrays. One file means one writer for the whole
  database. In practice the `fsync` dominates a write by orders of magnitude, so
  serialising the append itself changes little.
- **A query holds the read lock only to take its snapshot**, then scans lock-free
  (§3). It therefore sees a **point-in-time view**: documents inserted after the
  query started are not visible to it.
- **`*DB` and `*Collection` are safe for concurrent use** by any number of
  goroutines.

**Not for v1, and worth keeping in mind:**

- **No multi-process access.** There is no file lock, so two processes opening one
  file will corrupt it. `flock` is the cheap fix; *sharing* a file between
  processes needs a WAL and is a much larger change.
- **No multi-document atomicity.** `InsertMany` and `DeleteMany` land a prefix on
  a crash and report how many made it.
- **Sync policy is per-database, not per-thread.** With `SyncNever`, "how much can
  I lose" is a property of the whole database. Per-caller durability control
  would belong as a per-call flag, not as more `Open` options.

---

## 6. What one file costs, and what it buys

A trade, not a free win.

**Costs**

- **One append point means one writer for the whole database.** Writes to
  different collections serialise on one mutex.
- **Scan locality suffers when collections interleave** — a sequential read
  becomes a forward-only strided one ([`file-format.md`](file-format.md)).
- **Dropping a collection needs compaction** rather than deleting a file.

**Buys**

- The database is one artifact. Copy it, diff it, attach it to a bug report.
  This is most of what makes SQLite pleasant, and it is the point of the name.
- One header, one CRC scheme, one recovery rule, one torn tail to reason about —
  instead of N files that can disagree about how far they got.
- **Multi-collection atomicity becomes reachable.** Two documents in different
  collections land in one ordered stream, so the reserved begin/commit records
  are enough. With a file per collection there is no common order to commit
  against.

---

## 7. Layout

The root directory is the `nosqlite` package itself — in Go the import path *is*
the directory path, so a library's public API lives at the top. Everything under
`internal/` the compiler refuses to let another module import.

```
nosqlite.go        DB, Open, Close, Collection, options
collection.go      Insert, Replace, Delete, Find, FindOne, ForEach, Count
store.go           file header, record framing, append, replay, torn-tail recovery
catalog.go         collection name <-> id
index.go           offsets/lengths arrays, snapshots, lazy _id table
scan.go            streaming scan: sequential vs strided reads
query.go           Query, SortKey, Matcher — the query engine's public face
trace.go           the trace file
id.go              _id generation

internal/engine/   the query engine, which knows nothing about files or locks
  compile.go       filter document -> Matcher tree
  match.go         Matcher tree: and/or/not/cmp/in/exists, path walking
  compare.go       cross-type total ordering
  projection.go    field selection, applied during copy-out
  sort.go          SortKey, result ordering, bounded heap for sort+limit

cmd/nsq/             inspection CLI
capi/                C ABI (JSON in, JSON out, integer handles)
python/nosqlite/     ctypes wrapper
typescript/nosqlite/ koffi wrapper (ffi.ts) + Database/Collection (index.ts)
examples/basic/      main.go, basic.py, basic.ts — the same tour three times
conformance/         one fixture set, three runners
```

**`internal/engine` is the one real boundary in the codebase.** Hand that package
a decoded document and it will tell you whether the document matches and how it
sorts. It never opens a file, takes a lock, or knows a collection exists.

---

## 8. Roadmap — the extension points

Ordered by design dependency. [`todo.md`](todo.md) is the task view and wins on
what to do next; this section wins on mechanism.

1. **Compaction** — `Compact()` rewrites the file keeping only live versions,
   regrouped per collection. `DropCollection` falls out of it. No format change.
   Designed in [`compaction.md`](compaction.md).
2. **Secondary indexes** — a planner walking the Matcher tree to turn a `cmpNode`
   or `inNode` on an indexed field into a lookup, with the rest as a residual
   filter. Indexes rebuild from the log on open, so nothing new goes on disk.
3. **Partial parsing** — `Matcher.RequiredPaths()`, so the scan pulls only the
   fields the query touches out of the raw JSON and skips the full unmarshal for
   documents that don't match. The single biggest scan-speed win available, and
   it needs no format or API change. With projections landed, the required set is
   filter-fields ∪ projected-fields, so even a *matching* document need not be
   fully decoded.
4. **Batched fetch across the C ABI** — `nsq_find_batch`, so Python and
   TypeScript get `ForEach`-equivalent flat memory instead of one giant JSON
   string ([`bindings.md`](bindings.md)).
5. **Multi-process exclusion** — `flock` on the database file.
6. **Multi-document atomicity** — begin/commit records on the reserved `op=5`/`op=6`.
7. **`$elemMatch`, `$all`, `$size`, `$regex`** — new Matcher node types.
8. **External merge sort** — only if an unlimited `Sort` over a huge match set
   turns out to be a real workload rather than a mistake.

---

## 9. Open questions

- **Is a 1–3 s full scan of a million documents acceptable?** Deciding needs a
  number, not an opinion — benchmark `encoding/json` over the real record size.
  Partial parsing (§8) changes no formats and no APIs, so the decision can wait
  for evidence.
- **Does the trace file need a machine-readable form?** A JSON Lines mode would
  let traces be analysed rather than read — worth it only if trace-driven tooling
  actually appears.
- **Should the trace default to `writes` while the project is young?** The cost is
  one buffered line per insert; the alternative is discovering you needed it
  after the bug.

---

## See also

- [`file-format.md`](file-format.md) · [`records.md`](records.md) ·
  [`filters.md`](filters.md) · [`compaction.md`](compaction.md) — the internals
- [`api.md`](api.md) · [`bindings.md`](bindings.md) — the surfaces
- [`testing.md`](testing.md) — how any of this is verified
