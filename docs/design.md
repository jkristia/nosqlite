# nosqlite — design

An embedded document store in Go, in the spirit of SQLite: no server, no daemon, one file
on disk, linked directly into the host process.

**Supported today: insert, query** (filter / sort / skip / limit) **and replace.**
Everything else is deferred — but each deferred feature has a named extension point
below, so adding it is additive rather than a rewrite.

Read [`nosql-primer.md`](nosql-primer.md) first if the terms *collection* and *document*
aren't yet familiar; this document assumes them.

---

## 1. Goals and non-goals

**Goals**

- Embedded. No network, no separate process. `Open(path)` and you have a database.
- Mongo-dialect JSON filters, so the Go, Python, and TypeScript APIs share one query
  language with no translation layer.
- Go standard library only. No third-party dependencies.
- Durable by default: a completed `Insert` survives `kill -9`.
- Callable from Python and TypeScript through a C ABI.
- **Small, flat memory footprint.** A million documents costs ~12 MB of index; the
  documents themselves stay on disk and are streamed past, never all resident (§4).
  Datasets much larger than RAM are fine.
- Small enough to read in an afternoon.

**Non-goals for v1** — deliberately, not accidentally:

- Deletes, projections, aggregation. Replace exists and is whole-document; the space a
  superseded record holds is not reclaimed, because compaction does not exist yet.
- Secondary indexes (all queries are full scans; §5 says how indexes slot in). This is
  the price of the memory target: queries are I/O- and parse-bound until indexes land.
- Transactions, multi-document atomicity.
- Multi-process access (§6), replication, networking.

---

## 2. Data model

```
./demo.nsq              ← the database. One file. Everything is in here.
./demo.nsq.trace        ← human-readable trace of every operation (optional, §3)
```

**One file per database**, the way a `.sqlite` file is one file — you can copy it, mail
it, or check it into a fixture directory and it is the whole database. `Open("./demo.nsq")`
creates it if missing. All collections live inside it, their records interleaved in write
order and tagged with a collection id (§3).

- A **database** is one file.
- A **collection** is a named group of records within that file. Created on first insert.
  Names are `[A-Za-z0-9_-]{1,64}` and are recorded in the file by a catalog record (§3),
  so the list of collections survives a restart even for a collection with no documents.
- A **document** is any JSON object. It must have a string `_id`, unique within its
  collection; if the caller doesn't supply one, `nosqlite` generates it.

**A note on the word "log".** The database file *is* an append-only log internally, and
the trace file is also called a log in normal speech. To keep them apart, this document
says **database file** for the data and **trace file** for the human-readable debugging
output. They are never the same thing, and the trace file is never read back by the
database.

**`_id` generation.** 16 bytes rendered as 32 lowercase hex characters: 8 bytes of big-
endian Unix nanoseconds followed by 8 bytes from `crypto/rand`. Time-prefixed means ids
sort by creation order (useful as a cheap default sort and later as a range-scannable
key); the random tail makes collisions a non-issue. Caller-supplied ids are accepted as
long as they're non-empty strings.

**Type mapping.** Documents are held as `map[string]any` decoded by `encoding/json`, so:

| JSON | Go | Python |
| --- | --- | --- |
| object | `map[string]any` | `dict` |
| array | `[]any` | `list` |
| string | `string` | `str` |
| number | `float64` | `int` / `float` |
| bool | `bool` | `bool` |
| null | `nil` | `None` |

The `float64` row is the one to remember: **JSON has no integer type**, so `42` round-
trips through Go as `float64(42)`. Comparison and sorting treat all numbers as one type
(§5), and the Python wrapper's `json.loads` will hand back `42` as an `int` again, so
this is invisible from Python. It is visible from Go, and is documented in the Go API.

---

## 3. Storage engine — the database file and the trace file

The database file is a fixed header followed by a sequence of self-describing records.
Nothing is ever overwritten in place; a write is always an append.

### File header

The first 32 bytes, written once at creation:

```
┌──────────────────┬─────────────┬───────────┬──────────────┬──────────┐
│ magic "NSQLITE\n" │ format u16  │ flags u16 │ created u64  │ reserved │
│     8 bytes      │   2 bytes   │  2 bytes  │   8 bytes    │ 12 bytes │
└──────────────────┴─────────────┴───────────┴──────────────┴──────────┘
```

Records begin at offset 32. The magic means opening a JPEG as a database produces "not a
nosqlite file" rather than a confusing parse error, and `format` gives a clean refusal
when a future version changes the layout. `created` is Unix nanoseconds. The reserved
bytes are zero and are validated as zero, so they can be claimed later without ambiguity.

### Record framing

```
┌────────────┬────────┬──────────┬───────────┬────────────┬──────────────────┐
│ length u32 │ op u8  │ flags u8 │ coll u16  │ crc32 u32  │ payload (JSON)   │
│  4 bytes   │ 1 byte │  1 byte  │  2 bytes  │  4 bytes   │  `length` bytes  │
└────────────┴────────┴──────────┴───────────┴────────────┴──────────────────┘
   little-endian                        IEEE, over the header's op‖flags‖coll ‖ payload
```

- **12-byte header**, then exactly `length` bytes of payload.
- **`coll`** is the collection id — this is what makes a single file work. Every record
  says which collection it belongs to, so all collections interleave freely in one
  append-only stream and there is exactly one append point in the whole database.
- **`op`**:

  | op | meaning | supported |
  | --- | --- | --- |
  | 1 | insert — payload is the document | yes |
  | 4 | define collection — payload is `{"id":7,"name":"users"}` | yes |
  | 3 | replace — payload is the new document | yes |
  | 2 | delete tombstone — payload is the `_id` | reserved |
  | 5 / 6 | begin / commit, for multi-document atomicity | reserved |

  Reserving the values is what keeps every roadmap item in §11 a pure append rather
  than a format change — replace landed without touching the format at all. An unknown op is a hard error on read, never a skip, so an old
  binary fails loudly against a newer file instead of silently ignoring deletes.
- **`flags`** is zero in v1 and validated as zero. It is where per-record concerns go —
  a compression bit is the obvious first claim.
- **`crc32`** catches bit rot and, more importantly, distinguishes a corrupt record from
  a torn one during recovery.

### The collection catalog

A `define collection` record is appended the first time a collection is named, mapping a
small integer id to its name. Replay reads these into a `map[string]uint16`, so:

- collection ids stay stable across restarts,
- a collection with zero documents still exists after reopening,
- `Collections()` is answered from memory, and
- collection names appear exactly once in the file rather than in every record.

Ids are assigned sequentially from 1. 65535 collections is the cap and is documented; if
that were ever a real limit the field would widen behind a `format` bump.

### Opening: replay

`Open` validates the header, then scans the records sequentially from offset 32,
dispatching each by `coll` into that collection's index (§4). Cost is O(file size) and it
is the only full read of the file in normal operation.

**Torn trailing record.** A crash mid-append leaves a partial record at the end of the
file: fewer than 12 header bytes, or a header promising more payload than remains, or a
CRC mismatch on the last record. Because appends are the only writes, damage can only
ever be at the tail, so recovery is unambiguous:

- A short or CRC-failing record **at EOF** is treated as a never-completed write. The
  file is truncated back to the end of the last good record and open succeeds. The
  insert that was in flight is simply lost, which is correct — it never returned success
  to the caller.
- A CRC failure **not** at EOF is real corruption. `Open` returns an error rather than
  guessing; a repair tool is a follow-up.

### Durability

`Insert` writes the record with a single `Write` call and then, by default, calls
`f.Sync()` before returning. That is the "a completed Insert survives `kill -9`" promise,
and it costs a disk flush per insert.

An option relaxes it for bulk loading:

```go
nosqlite.Open(path, nosqlite.WithSync(nosqlite.SyncNever))   // fsync only on Close
```

`SyncAlways` (default) and `SyncNever` are the two v1 modes; a `SyncInterval(d)` mode is
an obvious later addition. Functional options are used from the start so adding modes
never breaks the `Open` signature.

`InsertMany` writes all records into one buffer, issues one `Write`, and syncs once — so
bulk loading is fast without giving up durability. It is *not* atomic: a crash can leave
a prefix of the batch on disk, and the return value tells you how many were written.

### What one file costs, and what it buys

Worth being explicit, because this is a trade and not a free win.

**Costs**

- **One append point means one writer for the whole database.** Writes to different
  collections no longer proceed in parallel; they serialise on a single mutex (§6). For
  an embedded single-process store this is close to irrelevant — the `fsync` dominates
  anyway — but it is a real regression against a file-per-collection layout.
- **Scan locality suffers when collections interleave.** Reading one collection walks
  ascending offsets that may be scattered among other collections' records, so a
  sequential read becomes a forward-only strided one. At ~300-byte documents and 4 KB
  pages, a few interleaved collections still mostly hit pages the readahead already
  fetched; heavy interleaving is what `Compact()` fixes by regrouping records per
  collection.
- **Dropping a collection needs compaction** rather than deleting a file.

**Buys**

- The database is one artifact. Copy it, diff it, attach it to a bug report, delete it.
  This is most of what makes SQLite pleasant, and it is the point of the name.
- One header, one CRC scheme, one recovery rule, one torn tail to reason about — instead
  of N files that can disagree with each other about how far they got.
- **Multi-collection atomicity becomes reachable.** Two documents in different
  collections land in one ordered stream, so the begin/commit records reserved above are
  enough. With a file per collection there is no common order to commit against, and
  cross-collection atomicity would need a separate WAL.

### The trace file

Separate from all of the above, and never read by the database: a plain-text record of
every operation, written next to the database file as `<dbpath>.trace`. This exists for
one purpose — being able to see what the database actually did — and it is deliberately
not a binary format, not a rotation scheme, and not a metrics system.

**Off by default**, because it roughly doubles write syscalls. Two ways to turn it on,
and the second matters more:

```go
db, _ := nosqlite.Open("./demo.nsq", nosqlite.WithTrace(nosqlite.TraceAll))
```

```sh
NOSQLITE_TRACE=all ./myprogram        # no recompile, no code change
```

The environment variable is the one you will actually use at 2am. Levels are `off`
(default), `writes` (inserts, opens, closes, syncs, errors), `all` (adds queries with
their scan statistics), and `verbose` (adds document payloads, truncated at 512 bytes).

**Format** — one line per operation, aligned columns, greppable:

```
2026-08-09T14:23:01.412Z  000121  OPEN    -        path=./demo.nsq docs=1000000 colls=2       dur=884ms   ok
2026-08-09T14:23:01.455Z  000122  INSERT  users    _id=018f2c9a4e1b7d3f0000000000000001 off=309200144 len=287  dur=41µs    ok
2026-08-09T14:23:01.455Z  000123  SYNC    -                                                    dur=1.9ms   ok
2026-08-09T14:23:02.101Z  000124  FIND    users    filter={"age":{"$gte":30}} sort=age:desc limit=10
                                           scanned=1000000 matched=41871 returned=10           dur=1.284s  ok
2026-08-09T14:23:02.140Z  000125  INSERT  orders   _id=ord-1                                   dur=12µs    ERROR duplicate _id "ord-1"
```

The columns are timestamp, sequence number, operation, collection, details, duration,
outcome. Three of those details are the ones that make this worth building:

- **`scanned=` / `matched=` / `returned=`** on every query. With no indexes in v1, "why
  is this slow" is always answered by the ratio between those three numbers, and this is
  the closest thing to `EXPLAIN QUERY PLAN` the database has.
- **`off=` and `len=`** on every write. That is the exact byte range of the record in the
  database file, so a trace line can be taken straight to `nsq dump` (§10) or a hex
  editor. Correlating "the thing the API did" with "the bytes on disk" is the whole
  debugging loop for a storage engine.
- **A monotonic sequence number**, so concurrent operations from several threads can be
  put back in order — wall-clock timestamps alone can't do that at microsecond spacing.

**Rules it must obey**, in priority order:

1. **A trace failure never fails an operation.** Write errors are counted and reported
   once on `Close`; the database carries on. Tracing is diagnostics, not durability.
2. **Line-buffered, never `fsync`ed.** If you are tracing a crash you need the lines that
   preceded it, so each line is flushed to the OS on write — but paying an `fsync` per
   line would make the trace slower than the database.
3. **Emitted after the operation completes**, carrying its outcome and duration. Ordering
   between threads comes from the sequence number, not from write order.
4. **Size-capped** (default 64 MB). On reaching the cap it writes one final
   `TRACE TRUNCATED` line and stops, rather than filling the disk. Truncated at `Open`
   by default; `WithTraceAppend()` keeps history across runs.
5. **Its own mutex**, independent of the data locks, so tracing a lock-free read (§4)
   does not reintroduce a lock on the read path.

---

## 4. Memory model — an offset index, not a document cache

**Design target: 1,000,000 documents in roughly 12 MB of process memory, with the
documents themselves never all resident at once.**

Documents live on disk. Memory holds only what is needed to *find* a record, and the
minimum for that is where it starts and how long it is.

```go
type DB struct {
    mu      sync.RWMutex          // guards appends and the catalog
    file    *os.File              // the one database file
    size    int64                 // current file size = next append offset
    catalog map[string]*Collection
    trace   *tracer               // nil when tracing is off
}

type Collection struct {
    db   *DB
    name string
    id   uint16                   // as written in each record header (§3)

    // The whole index: parallel arrays, one element per document, in insertion order.
    offsets []int64               // byte offset of each record's payload in db.file
    lengths []uint32              // payload length

    ids *idTable                  // nil until something actually needs _id lookup
}
```

That is 12 bytes per document. Nothing else is retained.

The offsets point into the single shared file (§2), so a collection is nothing but a name,
an id, and a sorted list of places to look. There is one `*os.File` for the whole database
and one lock guarding the single append point; readers need neither (see below).

### Why parallel arrays rather than `[]struct` or a map

Three reasons, in order of how much they matter:

1. **The Go GC never scans them.** `[]int64` and `[]uint32` contain no pointers, so the
   garbage collector treats each as one opaque block regardless of length. A
   `map[string]*entry` with a million entries is a million string headers plus a million
   pointers for the collector to walk on *every* cycle — that shows up as sustained CPU
   burn and pause time, and it is the single biggest reason the previous shape didn't
   scale. Pointer-free is the whole trick.
2. **Two allocations total**, not two million. `append` amortises growth; there is no
   per-document heap object.
3. Sequential scans touch `offsets`/`lengths` linearly, which is cache-friendly. A
   `[]struct{offset int64; length uint32}` would pad to 16 bytes; splitting them saves
   25% and keeps each array dense.

### What is deliberately *not* in memory

- **Parsed documents.** No document cache, no LRU. A read parses from disk and the
  result is discarded unless it matched.
- **An `_id` → position map**, unless earned. Generated ids are 128-bit with 64 random
  bits, so a collision is not a thing that happens; nothing needs to check them. The
  `idTable` is built lazily, on the first insert with a caller-supplied `_id` or the
  first by-id lookup. Workloads that only ever use generated ids never pay for it.

  When built, it is an open-addressed table sized to the next power of two ≥ 2N:
  `fingerprints []uint64` (a 64-bit hash of the id) plus `positions []uint32` (index into
  `offsets`). Also pointer-free. A fingerprint match is verified by reading that one
  record and comparing the real `_id`, so there are no false positives, only a rare extra
  read. Cost is 12 bytes per slot ≈ 24 bytes per document.

- **A read cache.** The operating system already has one: the page cache holds the log's
  hot pages, shared across processes, evictable under pressure, and not counted against
  this process's heap. Re-implementing that badly inside the database is the classic
  mistake. `pread` into a reused buffer is the right primitive.

### Memory budget, 1M documents averaging 300 B of JSON

| | Bytes/doc | 1M docs |
| --- | --- | --- |
| `offsets` | 8 | 8 MB |
| `lengths` | 4 | 4 MB |
| scan buffer (one, reused) | — | 64 KB |
| result set | — | O(`Limit`) |
| **Steady state** | **12** | **≈ 12 MB** |
| `idTable`, if built | +24 | +24 MB |
| *(for reference)* database file on disk | 312 | 312 MB |
| *(for reference)* previous all-in-RAM design | ~1500–3000 | 1.5–3 GB |

The ceiling this leaves is ~85M documents per GB of index, and the data itself is bounded
only by disk. If 12 bytes ever becomes the problem, `offsets` can be delta-encoded and
`lengths` dropped (the record header on disk already carries the length, at the cost of
one extra `pread` per document) — but that is a long way off and is not worth doing now.

### Replay cost at open

Rebuilding the index does **not** unmarshal anything. It walks the file reading 12-byte
headers, appends `(offset, length)` to the array named by `coll`, verifies each CRC over
the payload bytes, and moves on — no JSON parsing, no allocation per record. The one
exception is `define collection` records (§3), which are small, rare, and parsed. For a
312 MB file that is one sequential read plus a hardware-accelerated CRC32 pass, on the
order of a second.

One file helps here: replay is a single sequential pass over one file, and every
collection's index is built in that pass.

`WithFastOpen()` skips the CRC verification and uses `bufio.Reader.Discard` to jump over
payloads, checking only the final record for a torn tail. Faster, weaker: bit rot in the
middle of the file goes unnoticed until something reads that record.

### Readers do not need the lock

This falls out of the log being append-only, and it is worth stating because it shapes
§5 and §6: **bytes already written are never modified, and index entries `[0, n)` are
never rewritten** — appends only extend. So a reader takes the read lock just long enough
to copy three values — `n = len(offsets)`, the two slice headers, and `size` — and then
runs its entire scan with no lock held at all.

Reads go through `ReadAt`, which is `pread` underneath: it does not touch the file's
shared seek offset, so it is safe to use concurrently with an appending writer on the very
same `*os.File`. No second file handle, no reader lock, no writer starvation during a long
scan — and one handle for the whole database rather than one per collection.

Two read shapes, chosen by how much of the file the collection occupies:

- **A collection that dominates the file** (the common case — most databases have one
  big collection) reads through
  `bufio.NewReader(io.NewSectionReader(file, 32, snapshotSize-32))`, skipping records
  whose `coll` doesn't match. One buffered sequential pass.
- **A small collection sharing a large file** would waste that pass reading other
  collections' bytes, so it instead walks its own `offsets` and issues one `ReadAt` per
  record. The offsets are ascending, so this is a forward-only strided read that the
  page cache handles well.

The switch is a ratio — scan sequentially when the collection holds more than some
fraction of the file's records — and it is a pure optimisation: both paths return the
same documents in the same order.

The one rule this imposes: **the index slices may be appended to and reallocated, but
element `i` must never be written after publication.** Anything that would violate it —
in-place compaction, for instance — has to swap in a whole new pair of slices under the
write lock instead.

---

## 5. Query engine

### The filter compiles to a tree

A filter document is parsed **once**, into a tree of matchers, before any document is
examined:

```go
type Matcher interface {
    Match(doc map[string]any) bool
}
```

with implementations for the v1 operator set:

| Node | Filter syntax |
| --- | --- |
| `andNode` | multiple keys in one object; explicit `$and` |
| `orNode` | `$or` |
| `notNode` | `$not` |
| `cmpNode` | `$eq $ne $gt $gte $lt $lte` (and bare literals → `$eq`) |
| `inNode` | `$in`, `$nin` |
| `existsNode` | `$exists` |

An unknown `$operator` is a parse error, not a silent no-match — the alternative is a
typo'd filter quietly returning zero rows.

**Why a tree and not a closure or a switch inside the scan loop.** The tree is
*inspectable*. Adding indexes later means writing a planner that walks this tree looking
for a `cmpNode` or `inNode` on an indexed field, replacing that subtree's contribution
with an index lookup, and running the remainder as a residual filter over the far smaller
candidate set. With a closure there is nothing to inspect and indexes would require
rewriting the query path. This is the single most important structural decision in the
design, and it costs nothing in v1.

### Field access

`cmpNode` holds a pre-split path (`"address.city"` → `["address", "city"]`) and walks it
per document. A missing key yields a distinct "absent" sentinel — distinguishable from
`nil`, so `$exists` and `$eq: null` behave differently, matching the primer's §4.

Array semantics follow Mongo: if the value at a path is an array, a scalar comparison
matches when **any element** matches. `$elemMatch`, `$all`, and `$size` are not in v1.

### Cross-type ordering

Both comparison and sorting need a total order over values of different types, since
nothing prevents `age` from being a number in one document and a string in the next.
One function defines it:

```go
// compare returns -1, 0, or +1. Values of different types are ordered by type rank.
func compare(a, b any) int
```

Type ranks, lowest first:

```
absent  <  null  <  numbers  <  strings  <  booleans  <  arrays  <  objects
```

Two rules follow, and both are documented as behaviour rather than left to chance:

1. **Sorting** uses the full order, so a mixed-type field still sorts deterministically,
   and documents missing the sort field group together at the start.
2. **Comparison operators only match within a type.** `{"age": {"$gte": 30}}` does not
   match `{"age": "36"}`. This mirrors Mongo, and it is the sane choice — the alternative
   is inventing coercion rules that will surprise someone.

Equality via `$eq`/`$in` is deep for arrays and objects.

### Execution

Full scan, always, in v1 — but a *streaming* scan, so peak memory is a function of the
result size, not the collection size:

```
snapshot index under RLock, release
for each of this collection's records, in offset order (§4 picks the read shape):
      read payload into the reused scratch buffer
      json.Unmarshal into a scratch map        ← the only per-document allocation
      Matcher.Match
      no match → clear(scratch) and continue   ← document is dropped, never retained
      match    → deep-copy into the result
apply skip / limit
```

The scratch buffer grows to the largest record seen and is then reused; `clear()` on the
scratch map keeps its buckets between documents. A non-matching document costs one parse
and leaves nothing behind.

**Bounding the result set.** Three cases, and only the last is unbounded:

- **No sort.** The scan stops as soon as `Skip+Limit` documents have matched. Memory is
  O(`Limit`), and so is the work when the limit is small and matches are common.
- **Sort with a limit.** A bounded max-heap of size `Skip+Limit` (`container/heap`,
  ordered by the `SortKey`s via `compare`), pushing each match and dropping the worst
  when it overflows. Memory is O(`Skip+Limit`) regardless of how many documents match —
  a top-10 query over a million matching documents holds ten. Worth the ~40 lines.
- **Sort without a limit.** Every match must be materialised before it can be ordered.
  This is the one query shape that can still exhaust memory, it is inherent to the
  operation rather than to this design, and it is documented as such. Adding a `Limit`
  is the answer; an external merge sort is not v1.

`Find` returns a slice, so a filter matching a million documents produces a million
documents in the caller's memory no matter how frugal the engine was. `ForEach` (§7) is
the streaming escape hatch: it hands each match to a callback and retains nothing, which
is what a low-memory design actually needs and is far simpler than a cursor with its own
lifetime and locking rules.

Results are **deep-copied** out of the scratch map. The copy is what makes reuse of the
scratch map safe, and it also means a caller cannot corrupt anything by mutating a result.

**What this costs.** Every query parses every document: roughly 1–3 seconds for a full
scan of a million 300-byte records, dominated by `encoding/json`. That is the honest
trade for a 12 MB footprint, and it is the same trade SQLite makes before you add an
index. Two things fix it later, both already provisioned for: secondary indexes (§5, the
Matcher tree), and asking the tree which paths it needs so the scan can extract just
those fields from the raw JSON and skip the full unmarshal for non-matches (§11).

---

## 6. Concurrency

### The vocabulary

The property "each write either fully happens or doesn't happen at all" is **atomicity**
— the *A* in ACID. In document-store literature the specific guarantee here is called
**single-document atomicity**: one document's write is all-or-nothing, but there is no
guarantee spanning two documents unless you have transactions. The failure it rules out
is a **torn write** (also *partial write*, or *torn page* at the storage layer): a reader
or a restarted process observing half of a record.

Three neighbouring properties get confused with it, and it's worth keeping them apart
because this design handles them in three different places:

| Property | Means | Where it's handled |
| --- | --- | --- |
| **Atomicity** | A write is all-or-nothing. No half-documents, ever. | Single `Write` of one framed record + CRC (§3), under the collection lock. |
| **Isolation** | A concurrent reader never observes a write in progress. | `RWMutex` — the writer holds it exclusively. |
| **Durability** | A write that returned success survives a crash. | `fsync` policy (§3). |
| **Linearizability** | All writes appear to happen in one global order that matches real time. | Falls out of the single append point and its exclusive lock. |

Atomicity and durability are independent: `SyncNever` gives you atomic-but-not-durable
writes (a crash may lose the last N inserts, but never leaves half of one on disk).

### What v1 guarantees, in-process

- **One `sync.RWMutex` on the `DB`**, guarding the single append point, the file size,
  the catalog, and the index arrays. One file means one writer for the whole database,
  across all collections — the cost noted in §3. In practice the `fsync` dominates a
  write by orders of magnitude, so serialising the append itself changes little.
- **A query holds the read lock only to take its snapshot** — the collection's index
  length, its two slice headers, and the file size — and then scans with no lock held
  (§4). Because the file is append-only, those records are immutable, so the scan sees a
  consistent point-in-time view and a concurrent writer is never blocked by it. This is
  what keeps the single write lock from mattering: readers never contend for it beyond a
  few nanoseconds, so a query that runs for seconds still stalls nobody.
- The consequence: documents inserted *after* a query started are not visible to it.
  That is snapshot semantics, and it is the behaviour to document.
- **`*DB` and `*Collection` are safe for concurrent use by multiple goroutines.** This is
  stated in the package doc as an API promise, not left as an implementation detail.
- Each insert is one framed record written with one `Write` call while holding the write
  lock, so single-document atomicity holds within the process by construction. The CRC
  (§3) is what makes a torn record *detectable* if the process dies mid-`Write`; the
  truncate-on-open rule is what makes it *recoverable*.

### Not for v1 — keep in mind

None of the following is implemented in v1. They are listed here because each one is a
constraint on decisions being made now, not a feature to bolt on later.

**1. Threads calling in through the C ABI.** The Python and TypeScript bindings are the
realistic source of concurrency, more so than goroutines. `ctypes` releases the GIL
around a foreign call, so two Python threads genuinely execute inside Go at the same
time, on two different OS threads. The Go side handles this correctly already — cgo
entry from an arbitrary OS thread is fine, and the mutexes above do the real work — but
it means the C ABI must be treated as a **thread-safe, reentrant** surface from day one:
no package-level mutable state in `capi/`, and the handle table's mutex (§8) is
load-bearing rather than decorative.

**2. `nsq_close` racing in-flight calls.** The genuinely hard one, and it's created by
the FFI boundary rather than by the database. If one thread calls `nsq_close(h)` while
another is inside `nsq_find(h, ...)`, removing the handle frees a `*DB` whose file is
still being read. The fix is a refcount per handle: each call acquires the handle and
increments, `nsq_close` marks it closed and waits for the count to reach zero. Worth
designing in when the C layer is written, since retrofitting it means touching every
export.

**3. Multi-process access.** There is no file lock, so two processes opening the same
database file will corrupt it. The fix is `flock` on the database file itself, taken in
`Open`, released on `Close`, with stale detection for a crashed holder. The single-file
layout makes this simpler than it was going to be — one lock on one file, no directory
lock file to leave behind. That buys *exclusion*, not sharing: one process at a time.

Genuine multi-process **sharing** is a much larger change and is where the current design
would have to bend: the in-memory index (§4) would go stale the moment another process
appended, so readers would need to detect file growth and replay the tail, and the
truncate-on-torn-tail rule (§3) becomes unsafe when someone else may be mid-append. This
is the point at which a proper WAL — what SQLite actually does — stops being
over-engineering.

**4. Atomicity beyond one document.** `InsertMany` is explicitly *not* atomic (§3): a
crash can leave a prefix of the batch. Making a batch all-or-nothing needs either a
`begin`/`commit` record pair with commit-record semantics on replay, or a separate WAL.
The framing already has room — a new `op` value, no format change — which is the reason
§3 spends a byte on `op` in v1.

**5. Sync policy is per-database, not per-thread.** With `SyncNever` and concurrent
writers, "how much can I lose" is a property of the whole database, not of one writer's
stream. If per-caller durability control is ever wanted, it belongs as a per-call flag,
not as more `Open` options.

---

## 7. Go public API

Everything v1 exposes:

```go
package nosqlite

// --- database ---

func Open(path string, opts ...Option) (*DB, error)   // path is the database FILE
func (db *DB) Close() error
func (db *DB) Collection(name string) (*Collection, error)  // creates on demand
func (db *DB) Collections() []string
func (db *DB) Path() string

type Option func(*config)
func WithSync(mode SyncMode) Option
func WithFastOpen() Option              // skip CRC verification during replay (§4)
func WithTrace(level TraceLevel) Option // trace file, default TraceOff (§3)
func WithTraceFile(path string) Option  // default is <dbpath>.trace
func WithTraceAppend() Option           // keep history instead of truncating at Open
func WithTraceMaxBytes(n int64) Option  // default 64 MB

type SyncMode int
const (
    SyncAlways SyncMode = iota  // fsync every write (default)
    SyncNever                   // fsync only on Close
)

type TraceLevel int
const (
    TraceOff TraceLevel = iota  // default; NOSQLITE_TRACE overrides
    TraceWrites                 // inserts, opens, closes, syncs, errors
    TraceAll                    // + queries with scanned/matched/returned
    TraceVerbose                // + document payloads, truncated
)

// --- writing ---

func (c *Collection) Insert(doc map[string]any) (id string, err error)
func (c *Collection) InsertJSON(raw []byte) (id string, err error)
func (c *Collection) InsertMany(docs []map[string]any) (ids []string, err error)

// --- reading ---

type Query struct {
    Filter map[string]any  // Mongo-dialect filter; nil or empty matches everything
    Sort   []SortKey       // applied in order; empty means insertion order
    Skip   int
    Limit  int             // 0 means no limit
}

type SortKey struct {
    Field string           // dotted path
    Desc  bool
}

func (c *Collection) Find(q Query) ([]map[string]any, error)
func (c *Collection) FindOne(filter map[string]any) (map[string]any, error) // nil if none
func (c *Collection) Count(filter map[string]any) (int, error)
func (c *Collection) Name() string
func (c *Collection) Len() int      // documents in the collection, from the index — no I/O

// Streaming: fn is called with each match in turn and the document is not retained.
// Return a non-nil error (or ErrStop) to halt the scan early.
func (c *Collection) ForEach(q Query, fn func(doc map[string]any) error) error
```

Usage:

```go
db, err := nosqlite.Open("./demo.nsq", nosqlite.WithTrace(nosqlite.TraceAll))
defer db.Close()

users, _ := db.Collection("users")
users.Insert(map[string]any{
    "name": "Ada", "age": 36, "tags": []any{"math"},
})

docs, _ := users.Find(nosqlite.Query{
    Filter: map[string]any{"age": map[string]any{"$gte": 30}},
    Sort:   []nosqlite.SortKey{{Field: "age", Desc: true}},
    Limit:  10,
})

// Constant memory over any number of matches.
err = users.ForEach(nosqlite.Query{Filter: map[string]any{"age": map[string]any{"$gte": 30}}},
    func(doc map[string]any) error {
        fmt.Println(doc["name"])
        return nil
    })
```

**`Find` versus `ForEach`.** `Find` materialises its results, so a filter matching a
million documents returns a million documents — the engine's 12 MB footprint (§4) says
nothing about what the *caller* asks for. `Find` is the convenient one and is bounded
whenever `Limit` is set. `ForEach` is the one to reach for over a large result set: it
retains nothing between callbacks, so memory stays flat.

`ForEach` is a callback rather than a cursor deliberately. A cursor would need its own
lifetime, a `Close`, and rules about what happens when it outlives a write — whereas the
callback's scan is bounded by the call, and §4's snapshot means it needs no lock at all.
A real cursor only becomes necessary for streaming across the C ABI, which is a §11 item.

**Deliberately absent**, with their eventual signatures noted so today's code doesn't
foreclose them:

```go
func (c *Collection) Delete(filter map[string]any) (int, error)           // op=2 tombstones
func (c *Collection) DeleteMany(filter map[string]any) (int, error)
func (c *Collection) Update(filter, update map[string]any) (int, error)   // later: $set, $inc
func (db *DB) Compact() error                                            // rewrite live records
func (c *Collection) EnsureIndex(field string) error                     // planner in §5
// Query.Project map[string]any
```

Already present:

```go
// Whole-document write, so Mongo's name for it is Replace, not Update — see
// updates-and-compaction.md §9.1. It caps at one document, and there is
// deliberately no ReplaceMany (§8.1 of that doc).
func (c *Collection) Replace(filter, doc map[string]any) (int, error)  // op=3 records

func (db *DB) DeadBytes() int64  // bytes held by records a later replace superseded
func (db *DB) Size() int64
func ScanLive(path string) (LiveStats, error)  // the same accounting, offline
```

`Replace` leaves the superseded record in the file. Nothing reclaims that space yet —
`Compact` is step 8 of updates-and-compaction.md §8 — so a database that is replaced in
repeatedly grows without bound.

---

## 8. C ABI layer

`capi/` is `package main` built with `-buildmode=c-shared`, producing
`libnosqlite.so` / `.dylib` / `.dll` plus a generated header.

```go
//export nsq_open
func nsq_open(path, optsJSON *C.char) *C.char
                                          // opts, all optional, may be NULL:
                                          //   {"sync":"always"|"never",
                                          //    "trace":"off"|"writes"|"all"|"verbose",
                                          //    "trace_file":"...", "fast_open":true}
                                          // → {"handle":1} | {"error":"..."}

//export nsq_close
func nsq_close(h C.longlong) *C.char     // → {"ok":true}  | {"error":"..."}

//export nsq_insert
func nsq_insert(h C.longlong, coll, docJSON *C.char) *C.char
                                          // → {"id":"..."} | {"error":"..."}
//export nsq_insert_many
func nsq_insert_many(h C.longlong, coll, docsJSON *C.char) *C.char
                                          // → {"ids":[...]} | {"error":"..."}
//export nsq_replace
func nsq_replace(h C.longlong, coll, filterJSON, docJSON *C.char) *C.char
                                          // → {"replaced":1} | {"error":"..."}
//export nsq_find
func nsq_find(h C.longlong, coll, queryJSON *C.char) *C.char
                                          // → {"docs":[...]} | {"error":"..."}
//export nsq_count
func nsq_count(h C.longlong, coll, filterJSON *C.char) *C.char
                                          // → {"count":3}   | {"error":"..."}
//export nsq_collections
func nsq_collections(h C.longlong) *C.char // → {"names":[...]} | {"error":"..."}

//export nsq_free
func nsq_free(s *C.char)
```

Three rules make this boundary boring, which is what you want from an FFI boundary:

1. **JSON in, JSON out.** Every argument is a C string of JSON (or a plain string for
   `path`/`coll`), every return is a C string of JSON. One marshalling convention,
   nothing to keep in sync as the API grows, and every Go method in §7 is reachable.
2. **Never panic across the boundary.** Each exported function wraps its body in
   `defer recover()` and converts anything — including a Go panic — into
   `{"error": "..."}`. A panic crossing into C is undefined behaviour.
3. **Handles, not pointers.** A `map[int64]*DB` guarded by a mutex maps opaque integer
   handles to databases. Go pointers may not be stored in C memory or held by C across
   calls (cgo's pointer-passing rules; the garbage collector moves and frees things it
   can't see references to). An integer handle sidesteps this entirely, and also gives a
   clean error for use-after-close instead of a segfault.

Return strings are allocated with `C.CString` — that is C heap memory, invisible to the
Go GC — so **the caller must call `nsq_free` on every returned pointer**. This is the one
piece of manual discipline in the design, and §9 hides it completely.

**Memory across the boundary.** `nsq_find` materialises its whole result as one C string,
and that string is a second full copy of the data on top of the Go slice that produced it.
So the §4 footprint guarantee stops at the ABI: a `find` with no `limit` over a large
collection is the one way to blow up a process that is otherwise using 12 MB. v1's answer
is documentation plus a default `limit` in the language bindings; the real fix is a
batched or cursor-based `nsq_find_batch(h, coll, queryJSON, cursorID)` returning N
documents at a time, which is the `ForEach` equivalent for FFI and is a §11 item.

The same `.so` is what the TypeScript binding loads — `koffi` on Node, and the
same file would serve Bun's `bun:ffi` or Deno's `Deno.dlopen`. The JSON-in/JSON-out
convention means that binding is the same shape as the Python one, not a new design.

---

## 9. Python wrapper

The target usage, written down now so it can be judged before it exists:

```python
from nosqlite import Database

with Database("./demo.nsq", trace="all") as db:
    users = db["users"]

    users.insert({"name": "Ada", "age": 36, "tags": ["math"]})
    users.insert_many([
        {"name": "Grace", "age": 45},
        {"name": "Alan",  "age": 41},
    ])

    for u in users.find({"age": {"$gte": 40}}, sort=[("age", -1)], limit=10):
        print(u["name"], u["age"])

    print(users.find_one({"name": "Ada"}))       # dict, or None
    print(users.count({"age": {"$gte": 40}}))    # 2
    print(db.collections())                       # ['users']
```

It should feel like `pymongo` to anyone who has used it, and like `sqlite3` to anyone who
hasn't — `Database` opens, `db["name"]` gets a collection, the context manager closes.

**`python/nosqlite/_lib.py`** — the only place `ctypes` appears:

- Locates the shared library next to the package (`libnosqlite.so` / `.dylib` /
  `nosqlite.dll` chosen by `sys.platform`), raising a clear "did you run `make`?" error
  rather than an `OSError` if it's missing.
- Declares `argtypes`/`restype` for every export. `restype` is `ctypes.c_void_p`, **not**
  `c_char_p`: `c_char_p` makes ctypes copy the bytes and discard the pointer, leaking the
  C allocation with no way to free it. With `c_void_p` we keep the address, read it with
  `ctypes.string_at`, and pass it to `nsq_free`.
- Wraps every call in one helper:

  ```python
  def _call(fn, *args) -> dict:
      ptr = fn(*args)
      if not ptr:
          raise NoSQLiteError("library returned NULL")
      try:
          payload = json.loads(ctypes.string_at(ptr).decode("utf-8"))
      finally:
          _lib.nsq_free(ptr)          # always, even if json.loads raises
      if "error" in payload:
          raise NoSQLiteError(payload["error"])
      return payload
  ```

  The `try/finally` is the whole memory-safety story: no caller can forget to free.

**`python/nosqlite/__init__.py`** — `Database`, `Collection`, `NoSQLiteError`:

- `Database.__enter__`/`__exit__`, plus a `__del__` safety net, so the handle is always
  closed.
- `Collection.find(filter=None, sort=None, skip=0, limit=0)` builds the query dict
  (`sort=[("age", -1)]` → `[{"field": "age", "desc": True}]`) and returns a `list[dict]`.
- `Collection.find_one(filter)` is `find(filter, limit=1)` unwrapped to a single `dict`
  or `None` — Go's `FindOne` needs no C export of its own.
- `Collection.replace(filter, document)` → `nsq_replace`, returning the number replaced
  (`0` or `1`). A rejected `_id` change surfaces as `NoSQLiteError`, like any other
  error the boundary reports.
- `Database(path, sync="always", trace=None)` passes `{"sync": ..., "trace": ...}` as
  `nsq_open`'s options argument, reaching `WithSync` and `WithTrace` from §7. The
  `NOSQLITE_TRACE` environment variable works from Python too, since it is read on the
  Go side — which is the point of having it.
- `Collection.__len__` → `nsq_count` with an empty filter, so `len(users)` works.
- Strings cross as UTF-8 (`str.encode`), JSON via `json.dumps` / `json.loads`.

**Result-size safety.** `find()` builds a Python list, and `nsq_find` built a C string
before that, so an unlimited query over a large collection materialises the result three
times over (Go slice → C string → Python list). Two mitigations, both cheap:

- `find()` applies a **default `limit` of 1000** when the caller passes none, and raises
  if a truncated result is silently hit — explicit `limit=0` opts out. Surprising a user
  with a truncated list is bad; surprising them with 4 GB of RSS is worse, and the error
  makes it neither.
- `iter_find(filter, ..., batch=1000)` is a generator that pages with `skip`/`limit` and
  yields documents one at a time, so Python-side memory stays flat. It becomes the
  wrapper over `nsq_find_batch` once that exists (§11) with no change to its signature.

**Build.** A `Makefile` target, because the `.so` must exist before the package works:

```make
build:
	go build -buildmode=c-shared -o python/nosqlite/libnosqlite.so ./capi
```

---

## 10. Planned package layout

```
nosqlite/
├── go.mod                      module github.com/<user>/nosqlite
├── nosqlite.go                 DB, Open, Close, Collection, Collections, Option/SyncMode
├── collection.go               Insert, InsertJSON, InsertMany, Replace, Find, FindOne, ForEach, Count
├── store.go                    file header, record framing, append, replay, torn-tail recovery
├── catalog.go                  collection name ↔ id, define-collection records
├── index.go                    offsets/lengths arrays, snapshots, lazy idTable
├── scan.go                     streaming scan: sequential vs strided read, scratch reuse
├── trace.go                    tracer: levels, line format, sequence numbers, size cap
├── query.go                    Query, and the SortKey/Matcher/CompileFilter re-exports
├── id.go                       _id generation
├── *_test.go                   per-file unit tests
├── internal/
│   └── engine/                 the query engine — no files, no locks, no collections
│       ├── compile.go          filter document → Matcher tree
│       ├── match.go            Matcher interface, and/or/not/cmp/in/exists nodes, path walking
│       ├── compare.go          cross-type total ordering
│       └── sort.go             SortKey, result ordering, bounded heap for Sort+Limit
├── cmd/nsq/
│   └── main.go                 CLI: dump, stat, verify, find (§10)
├── capi/
│   └── capi.go                 package main, //export'ed C ABI, handle table
├── python/
│   └── nosqlite/
│       ├── __init__.py         Database, Collection, NoSQLiteError
│       ├── _lib.py             ctypes bindings, _call helper
│       └── libnosqlite.so      build artifact (gitignored)
├── typescript/
│   ├── package.json            one dependency: koffi (Node has no built-in FFI)
│   ├── tsconfig.json           type-check only; Node runs the .ts files as-is
│   └── nosqlite/
│       ├── index.ts            Database, Collection, NoSQLiteError
│       ├── ffi.ts              koffi bindings, call helper
│       └── libnosqlite.so      build artifact, copied by make (gitignored)
├── examples/
│   └── basic/                  one directory per example: a Go program is a
│       ├── main.go             directory, so two examples cannot share one
│       ├── basic.py
│       ├── basic.ts
│       └── package.json        only says {"type": "module"}, for basic.ts
├── docs/
│   ├── nosql-primer.md
│   └── design.md
└── Makefile                    build, test, example
```

Roughly 1500–1800 lines of Go for v1, tests included.

### The `nsq` inspection CLI

The other half of the debugging story, and cheap because it is the replay code (§4)
pointed at stdout instead of an index. The trace file says *what the API did*; `nsq dump`
says *what is actually in the file*, in the same vocabulary, so the `off=` field in a
trace line is directly greppable here:

```sh
nsq stat   demo.nsq                 # header, collections, record counts, file size
nsq dump   demo.nsq                 # every record: offset, len, op, collection, payload
nsq dump   demo.nsq --coll users --from 309200144 --limit 5
nsq verify demo.nsq                 # walk every CRC, report the first bad offset
nsq find   demo.nsq users '{"age":{"$gte":30}}' --sort age:desc --limit 10
```

`nsq verify` is the one that pays for itself: a corrupt record is otherwise only
discovered when a query happens to read it, and §3's rule is that a mid-file CRC failure
refuses to open the database at all.

---

## 11. Roadmap

Ordered, each pointing at the extension point left for it:

1. **Replace and delete** — `op = 2/3` records (§3) plus `Compact()` to rewrite the file
   keeping only live versions, regrouped by collection so scan locality is restored.
   `DropCollection` falls out of the same machinery. No format change. Designed in full in
   [updates-and-compaction.md](updates-and-compaction.md); steps 1-5 are built — snapshot
   captures the file handle, the `dead` byte counter, the per-collection `dirty` flag,
   `Collection.Replace`, and replay of the `op=3` records it writes. Delete and `Compact`
   remain.
2. **Secondary indexes** — a planner walking the Matcher tree (§5) to turn `cmpNode` /
   `inNode` on an indexed field into a lookup, with the rest as a residual filter.
   Indexes rebuild from the log on open; nothing new on disk.
3. **Projections** — `Query.Project`, applied during the copy-out in §5.
4. **Partial parsing** — `Matcher.RequiredPaths()`, so the scan pulls only the fields the
   filter touches out of the raw JSON and skips the full `json.Unmarshal` for documents
   that don't match. The single biggest win available for scan speed (§5), and it needs
   no format or API change. Pairs with a faster JSON path than `encoding/json`.
5. **Batched fetch across the C ABI** — `nsq_find_batch` so Python and TypeScript get
   `ForEach`-equivalent flat memory instead of one giant JSON string (§8, §9).
6. **Handle refcounting in the C ABI** — so `nsq_close` can't free a database out from
   under an in-flight call from another thread (§6, *not for v1* #2). Cheap if done when
   the C layer is written, invasive afterwards.
7. **Multi-process exclusion** — `flock` on the database file (§6, #3). Multi-process
   *sharing* is a much larger change and needs a WAL.
8. **Multi-document atomicity** — begin/commit records via a new `op` value (§6, #4).
9. ~~**TypeScript binding**~~ — done: `typescript/nosqlite`, same `.so`, same JSON
   convention (§8), loaded with `koffi` because Node has no FFI of its own.
10. **`$elemMatch`, `$all`, `$size`, `$regex`** — new Matcher node types (§5).
11. **External merge sort** — only if an unlimited `Sort` over a huge match set turns out
    to be a real workload rather than a mistake (§5).

---

## 12. Open questions

Worth settling before or during implementation:

- **Should `Insert` mutate the caller's map** by writing `_id` into it (pymongo does
  this, and it's convenient) or leave it untouched and only return the id (cleaner)?
  Current lean: leave it untouched, return the id.
- **Is `float64` for all numbers acceptable in the Go API**, or should integers be
  preserved via `json.Decoder.UseNumber()`? `UseNumber` keeps `42` an integer through a
  round trip but makes every comparison in §5 do a `json.Number` conversion. Current
  lean: `float64` for v1, documented; revisit if it bites.
- ~~**One log file per collection, or one file for the whole database?**~~ **Resolved:
  one file.** The database is a single `.nsq` file with collection-tagged records (§2,
  §3), plus an optional `.nsq.trace` beside it. Accepted costs: a single write lock
  across all collections, and interleaved scan locality until `Compact()` exists.
- **Should the trace file default to on in debug builds?** It is off by default and
  enabled by `NOSQLITE_TRACE` or `WithTrace`. There is an argument for defaulting to
  `writes` while the project is young, since the cost is one buffered line per insert and
  the alternative is discovering you needed it after the bug. Current lean: off, but make
  the env var impossible to miss in the README.
- **Does the trace file need a stable machine-readable form?** The aligned text above is
  for humans. A `TraceJSON` mode emitting JSON Lines would let the trace be analysed
  rather than read — worth it only if trace-driven tooling actually appears.
- **Is a 1–3 s full scan of a million documents acceptable for v1**, or does partial
  parsing (roadmap #4) need to be in the first cut? Deciding this needs a number, not an
  opinion: benchmark `encoding/json` over the real record size before committing. If the
  gap is large, `RequiredPaths()` moves into v1 — it changes no formats and no APIs, so
  the decision can be deferred until there is a benchmark to point at.
- **Should `Len()`/`Count(nil)` be exact after updates and deletes land?** With
  tombstones in the log, `len(offsets)` counts records, not live documents. Either the
  index tracks a live count, or `Count` stops being O(1). Cheap to get right now, awkward
  later.
