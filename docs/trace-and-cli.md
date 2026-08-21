# The trace file and the `nsq` CLI

Two halves of one debugging loop. The trace file says **what the API did**;
`nsq` says **what is actually in the file**, in the same vocabulary — a trace
line's `off=` is directly greppable in `nsq dump`.

---

## Turn the trace on

Off by default, because it roughly doubles write syscalls.

```sh
NOSQLITE_TRACE=all ./myprogram      # no recompile; works from Python and TypeScript too
```

```go
nosqlite.Open("./demo.nsq", nosqlite.WithTrace(nosqlite.TraceAll))
```

The environment variable is the one you will actually use at 2am. It overrides
the code, so it works without touching a build.

| level | writes |
|---|---|
| `off` | nothing (default) |
| `writes` | inserts, replaces, deletes, opens, closes, syncs, errors |
| `all` | + queries, with their scan statistics |
| `verbose` | + document payloads, truncated at 512 bytes |

The file lands at `<dbpath>.trace` — `WithTraceFile` moves it,
`WithTraceMaxBytes` resizes the cap, `WithTraceAppend` keeps history across runs
instead of truncating at `Open`.

## Read it

One line per operation: timestamp, sequence number, operation, collection,
details, duration, outcome.

```
2026-08-09T14:23:01.412Z  000121  OPEN    -       path=./demo.nsq docs=1000000 colls=2   dur=884ms  ok
2026-08-09T14:23:01.455Z  000122  INSERT  users   _id=018f2c…001 off=309200144 len=299   dur=41µs   ok
2026-08-09T14:23:01.455Z  000123  SYNC    -                                              dur=1.9ms  ok
2026-08-09T14:23:02.101Z  000124  FIND    users   filter={"age":{"$gte":30}} sort=age:desc limit=10
                                          scanned=1000000 matched=41871 returned=10      dur=1.284s ok
2026-08-09T14:23:02.140Z  000125  INSERT  orders  _id=ord-1                              dur=12µs   ERROR duplicate _id "ord-1"
```

Three details are what make it worth building:

- **`scanned=` / `matched=` / `returned=`** on every query. With no indexes, "why
  is this slow" is always answered by the ratio between those three numbers.
  This is the closest thing to `EXPLAIN QUERY PLAN` the database has.
- **`off=` and `len=`** on every write — the exact byte range of the record, so a
  trace line leads straight to `nsq dump --from <off>` or a hex editor.
- **A monotonic sequence number**, so concurrent operations from several threads
  can be put back in order. Wall-clock timestamps can't do that at microsecond
  spacing.

## Rules it obeys

1. **A trace failure never fails an operation.** Write errors are counted and
   reported once on `Close`. Tracing is diagnostics, not durability.
2. **Line-buffered, never `fsync`ed.** Tracing a crash needs the lines that
   preceded it, so each line is flushed to the OS — but an `fsync` per line would
   make the trace slower than the database.
3. **Emitted after the operation completes**, carrying its outcome and duration.
4. **Size-capped** (default 64 MB). At the cap it writes one `TRACE TRUNCATED`
   line and stops, rather than filling the disk.
5. **Its own mutex**, independent of the data locks, so tracing a lock-free read
   does not reintroduce a lock on the read path.

---

## `nsq`

```sh
make cli        # builds ./bin/nsq
```

| command | does |
|---|---|
| `nsq stat <file>` | header, collections, record counts, size, dead bytes |
| `nsq dump <file> [flags]` | every record: offset, len, op, collection, payload |
| `nsq verify <file>` | walk every checksum, report every bad record |
| `nsq find <file> <coll> [filter]` | run a query against the file |

```sh
./bin/nsq stat   demo.nsq
./bin/nsq dump   demo.nsq --coll users --limit 5
./bin/nsq dump   demo.nsq --from 309200144        # the off= from a trace line
./bin/nsq verify demo.nsq
./bin/nsq find   demo.nsq users '{"age":{"$gte":30}}' --sort age:desc --limit 10
./bin/nsq find   demo.nsq users --projection '{"name":1,"_id":0}'
```

**dump flags:** `--coll <name>`, `--from <offset>`, `--limit <n>`,
`--payload=false` (framing only).

**find flags:** `--sort <field:asc|desc>[,...]`, `--projection <json>`,
`--skip <n>`, `--limit <n>` (default 10; `0` means no limit).

**`verify` exits non-zero when it finds a bad record**, so it works in a script.
It is the one that pays for itself: a corrupt record is otherwise only discovered
when a query happens to read it, and a mid-file checksum failure refuses to open
the database at all.

`nsq stat` also prints the dead-byte total and suggests compacting when it is
worth doing ([`compaction.md`](compaction.md)).

---

## See also

- [`file-format.md`](file-format.md) — what `nsq dump` is showing you
- [`api.md`](api.md) — the trace options in each language
