# nosqlite

An embedded document store in Go, in the spirit of SQLite: no server, no daemon,
one file on disk, linked into the host process. Callable from Go, Python and
TypeScript.

```
./demo.nsq          the database — one file, all collections, copy it anywhere
./demo.nsq.trace    optional human-readable trace of every operation
```

**v1:** insert, query (filter / projection / sort / skip / limit), replace, delete.
No compaction yet, no indexes, no `$set`, no multi-process access.

---

## How to

**Build it.** Go 1.24+, a C compiler, and `make`. Full setup in
[getting-started.md](docs/getting-started.md).

```sh
make build      # the shared library — Python and TypeScript need it
make test       # go test ./...
```

**Use it from Go.**

```go
db, _ := nosqlite.Open("./demo.nsq")
defer db.Close()
users, _ := db.Collection("users")
users.Insert(map[string]any{"name": "Ada", "age": 36})
docs, _ := users.Find(nosqlite.Query{Filter: map[string]any{"age": map[string]any{"$gte": 30}}})
```

**Use it from Python.**

```python
with Database("./demo.nsq") as db:
    db["users"].insert({"name": "Ada", "age": 36})
    db["users"].find({"age": {"$gte": 30}})
```

**Use it from TypeScript.**

```ts
const db = new Database("./demo.nsq");
db.collection("users").insert({ name: "Ada", age: 36 });
db.collection("users").find({ age: { $gte: 30 } });
```

Every call is synchronous in all three languages — the database is in this
process, there is no socket to await. Full surface in [api.md](docs/api.md).

**See what it did.** No recompile, works from all three languages:

```sh
NOSQLITE_TRACE=all ./myprogram      # writes ./demo.nsq.trace
```

**Look inside the file.**

```sh
make cli && ./bin/nsq stat demo.nsq
```

**Run an example.** `make example`, `make example-py`, `make example-ts`.

---

## Docs

**Using it**

- [getting-started.md](docs/getting-started.md) — install, build, verify, troubleshoot
- [api.md](docs/api.md) — the full API in Go, Python and TypeScript
- [filters.md](docs/filters.md) — filter operators and projections, the query dialect
- [trace-and-cli.md](docs/trace-and-cli.md) — the trace file and the `nsq` tool
- [nosql-primer.md](docs/nosql-primer.md) — if *collection* and *document* are new terms

**How it works**

- [design.md](docs/design.md) — goals, memory model, concurrency, code layout, what it costs
- [file-format.md](docs/file-format.md) — the bytes on disk, replay, torn-tail recovery
- [records.md](docs/records.md) — what insert, replace and delete write, and to what
- [compaction.md](docs/compaction.md) — the garbage mutation leaves, and the plan to collect it
- [bindings.md](docs/bindings.md) — the C ABI, and how a TypeScript call reaches Go

**Working on it**

- [testing.md](docs/testing.md) — unit, conformance and scale tests across three languages
- [todo.md](docs/todo.md) — what is being built next
- [go-primer/](docs/go-primer/) — Go itself, if you come from TypeScript, Python or C#
- [compression.md](docs/compression.md) — measured, not built: what compressing payloads would buy

---

## What it costs

| | |
| --- | --- |
| Memory | ~12 bytes per document; documents stay on disk |
| 1M documents | ~12 MB of index, ~312 MB of file |
| Open | one sequential read, no JSON parsing |
| Query | full scan, ~1–3 s per million 300-byte documents |
| Insert | one append plus one `fsync` (`WithSync(SyncNever)` relaxes it) |

A completed `Insert` survives `kill -9`. A crash mid-append leaves a torn record
at the tail, which the next `Open` detects and truncates — that insert never
returned success, so losing it is correct.

## What it does not do

Field updates (`$set`), aggregation, secondary indexes, transactions,
multi-document atomicity, networking, compaction. **And no multi-process
access** — there is no file lock, so two processes opening one file will corrupt
it. Each of these has an extension point in [design.md](docs/design.md).

Within one process, `*DB` and `*Collection` are safe for concurrent use, and a
query sees a point-in-time snapshot.
