# nosqlite

An embedded document store in Go, in the spirit of SQLite: no server, no daemon,
one file on disk, linked directly into the host process. Callable from Go and
from Python.

**v1 supports exactly two operations: insert and query** (filter / sort / skip /
limit). See [`docs/design.md`](docs/design.md) for the full design, and
[`docs/nosql-primer.md`](docs/nosql-primer.md) if *collection* and *document*
are new terms.

```
./demo.nsq          the database — one file, all collections, copy it anywhere
./demo.nsq.trace    optional human-readable trace of every operation
```

---

## Quick start — Go

```go
db, err := nosqlite.Open("./demo.nsq")
if err != nil { log.Fatal(err) }
defer db.Close()

users, _ := db.Collection("users")
id, _ := users.Insert(map[string]any{"name": "Ada", "age": 36, "tags": []any{"math"}})

docs, _ := users.Find(nosqlite.Query{
    Filter: map[string]any{"age": map[string]any{"$gte": 30}},
    Sort:   []nosqlite.SortKey{{Field: "age", Desc: true}},
    Limit:  10,
})

// Constant memory over any number of matches:
users.ForEach(nosqlite.Query{Filter: map[string]any{"age": map[string]any{"$gte": 30}}},
    func(doc map[string]any) error {
        fmt.Println(doc["name"])
        return nil
    })
```

```sh
make example        # runs examples/basic
```

## Quick start — Python

```sh
make build          # builds python/nosqlite/libnosqlite.so — required first
make example-py     # runs examples/basic/basic.py
```

```python
from nosqlite import Database

with Database("./demo.nsq", trace="all") as db:
    users = db["users"]

    users.insert({"name": "Ada", "age": 36, "tags": ["math"]})
    users.insert_many([{"name": "Grace", "age": 45}, {"name": "Alan", "age": 41}])

    for u in users.find({"age": {"$gte": 40}}, sort=[("age", -1)], limit=10):
        print(u["name"], u["age"])

    print(users.find_one({"name": "Ada"}))    # dict, or None
    print(users.count({"age": {"$gte": 40}})) # 2
    print(len(users))                          # 3
    print(db.collections())                    # ['users']
```

The Python package is a thin `ctypes` wrapper over the same shared library a
future TypeScript binding would load. `find()` applies a **default limit of
1000** and raises if it is actually hit — use `limit=0` for everything, or
`iter_find()` to stream without holding the whole result in memory.

---

## Filters

The Mongo dialect, so the same filter works from Go, Python and the CLI.

| | |
| --- | --- |
| `{"name": "Ada"}` | bare literal means `$eq` |
| `{"age": {"$gte": 30, "$lt": 60}}` | `$eq $ne $gt $gte $lt $lte` |
| `{"name": {"$in": ["Ada", "Alan"]}}` | `$in`, `$nin` |
| `{"address": {"$exists": true}}` | `$exists` |
| `{"address.city": "Oslo"}` | dotted paths reach into nested objects |
| `{"tags": "math"}` | an array matches if **any element** matches |
| `{"$or": [{...}, {...}]}` | `$and`, `$or`, `$not` |

Two behaviours worth knowing:

- **Comparison operators only match within a type.** `{"age": {"$gte": 30}}`
  does not match `{"age": "36"}`. Sorting, by contrast, uses a total order
  across types: `absent < null < numbers < strings < booleans < arrays <
  objects`.
- **An unknown `$operator` is an error**, not a silent no-match. A typo'd
  `$gtee` returning zero rows is the worst possible outcome.

---

## The trace file

Off by default. Two ways to turn it on, and the second is the one you will
actually use:

```go
nosqlite.Open("./demo.nsq", nosqlite.WithTrace(nosqlite.TraceAll))
```

```sh
NOSQLITE_TRACE=all ./myprogram      # no recompile, works from Python too
```

Levels: `off`, `writes`, `all` (adds queries and their scan statistics),
`verbose` (adds document payloads). One line per operation:

```
2026-08-09T14:23:01.455Z  000122  INSERT  users   _id=018f2c…001 off=309200144 len=299   dur=41µs   ok
2026-08-09T14:23:02.101Z  000124  FIND    users   filter={"age":{"$gte":30}} sort=age:desc limit=10 scanned=1000000 matched=41871 returned=10  dur=1.284s  ok
```

`scanned` / `matched` / `returned` is the closest thing to `EXPLAIN QUERY PLAN`
this database has — with no indexes, the ratio between them explains every slow
query. `off=` / `len=` is the exact byte range of the record, so a trace line
leads straight to `nsq dump --from <off>`.

A trace failure never fails an operation, the file is never `fsync`ed, and it
stops at a size cap (64 MB default) rather than filling the disk.

---

## The `nsq` CLI

```sh
make cli

./bin/nsq stat   demo.nsq                     # header, collections, counts, sizes
./bin/nsq dump   demo.nsq --coll users --limit 5
./bin/nsq dump   demo.nsq --from 309200144    # the off= from a trace line
./bin/nsq verify demo.nsq                     # walk every checksum
./bin/nsq find   demo.nsq users '{"age":{"$gte":30}}' --sort age:desc --limit 10
```

`verify` exits non-zero when it finds a bad record, so it works in a script.

---

## What it costs

| | |
| --- | --- |
| Memory | ~12 bytes per document (`offsets` + `lengths`); documents stay on disk |
| 1M documents | ~12 MB of index, ~312 MB of file |
| Open | one sequential read; no JSON parsing during replay |
| Query | a full scan, ~1–3 s per million 300-byte documents, dominated by `encoding/json` |
| Insert | one append plus one `fsync` (`WithSync(SyncNever)` relaxes it) |

A completed `Insert` survives `kill -9`. A crash mid-append leaves a torn
record at the tail, which the next `Open` detects by checksum and truncates
away — that insert never returned success, so losing it is correct. A checksum
failure **not** at the tail is real corruption and `Open` refuses rather than
guessing.

## What it does not do

Updates, deletes, projections, aggregation, secondary indexes, transactions,
multi-document atomicity, networking. **And no multi-process access**: there is
no file lock, so two processes opening the same file will corrupt it. Each of
these has a named extension point in the design doc; §11 there is the ordered
roadmap.

Within one process, `*DB` and `*Collection` are safe for concurrent use.
Queries see a point-in-time snapshot: documents inserted after a query started
are not visible to it.

---

## Numbers are `float64`

JSON has no integer type, so `42` round-trips through Go as `float64(42)`:

```go
age := doc["age"].(float64)   // not int
```

Comparison and sorting treat all numbers as one type, and Python's `json.loads`
hands back `42` as an `int` again — so this is only visible from Go.

---

## Layout

The root directory is the `nosqlite` package itself — in Go the import path *is*
the directory path, so a library's public API lives at the top. Everything that
callers never touch sits under `internal/`, which the compiler refuses to let
any other module import.

```
nosqlite.go        DB, Open, Close, Collection, options
collection.go      Insert, InsertJSON, InsertMany, Find, FindOne, ForEach, Count
store.go           file header, record framing, append, replay, torn-tail recovery
catalog.go         collection name <-> id
index.go           offsets/lengths arrays, snapshots, lazy _id table
scan.go            streaming scan: sequential vs strided reads, query execution
query.go           Query, SortKey, Matcher — the query engine's public face
trace.go           the trace file
id.go              _id generation

internal/engine/   the query engine, which knows nothing about files or locks
  compile.go       filter document -> Matcher tree
  match.go         Matcher tree: and/or/not/cmp/in/exists, path walking
  compare.go       cross-type total ordering
  sort.go          SortKey, result ordering, bounded heap for sort+limit

cmd/nsq/           inspection CLI
capi/              C ABI (JSON in, JSON out, integer handles)
python/nosqlite    ctypes wrapper
examples/basic/    main.go (Go) and basic.py (Python), the same tour twice
```

The `internal/engine` split is the one real boundary in the codebase: hand that
package a decoded document and it will tell you whether the document matches and
how it sorts. It never opens a file, takes a lock, or knows a collection exists.

## Development

```sh
make test        # go test ./...
make test-race   # the concurrency tests are worth running under -race
make all         # fmt, vet, test, build the library, build the CLI
```

---

## Decisions taken from the design's open questions

- **`Insert` does not mutate the caller's map.** It returns the id; assign it
  yourself if you want it in your own dict.
- **All numbers are `float64`** in the Go API (no `json.Decoder.UseNumber()`),
  documented above rather than worked around.
- **Tracing is off by default**, enabled by `WithTrace` or `NOSQLITE_TRACE`.
- **Sorted results break ties by insertion order**, so a `Sort`+`Limit` query
  returns the same documents in the same order every run.
- **Handle refcounting is in the C ABI from the start**, so `nsq_close` cannot
  free a database out from under an in-flight call on another thread — it waits
  for the call to finish, and calls arriving after the close get a clean error.
  (Design §6 flagged this as cheap now, invasive later.)
