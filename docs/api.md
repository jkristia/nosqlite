# The API

The same database from three languages. Filters and projections are the Mongo
dialect everywhere — [`filters.md`](filters.md) is the reference for what goes
inside them.

Every call is **synchronous**. The database is linked into the process; there is
no socket and nothing to await.

---

## How to

| | Go | Python | TypeScript |
|---|---|---|---|
| open | `nosqlite.Open(path, opts...)` | `Database(path, **opts)` | `new Database(path, opts)` |
| close | `db.Close()` | `with` block, or `db.close()` | `try/finally`, or `using` |
| get a collection | `db.Collection("users")` | `db["users"]` | `db.collection("users")` |
| list collections | `db.Collections()` | `db.collections()` | `db.collections()` |
| insert one | `Insert(doc)` → id | `insert(doc)` → id | `insert(doc)` → id |
| insert many | `InsertMany(docs)` | `insert_many(docs)` | `insertMany(docs)` |
| overwrite one | `Replace(filter, doc)` → n | `replace(filter, doc)` | `replace(filter, doc)` |
| delete one | `Delete(filter)` → n | `delete(filter)` | `delete(filter)` |
| delete all matches | `DeleteMany(filter)` → n | `delete_many(filter)` | `deleteMany(filter)` |
| query | `Find(Query{...})` | `find(filter, **opts)` | `find(filter, opts)` |
| first match | `FindOne(filter)` | `find_one(filter)` | `findOne(filter)` |
| count | `Count(filter)` | `count(filter)`, `len(coll)` | `count(filter)` |
| stream results | `ForEach(q, fn)` | `iter_find(filter, ...)` | `iterFind(filter, opts)` |

Three rules that hold in all three languages:

- **`Replace` overwrites the whole document.** Fields the replacement omits are
  gone — it is not a merge, and there is no `$set` yet.
- **`_id` is immutable.** A replacement may repeat the matched document's `_id`
  but not carry a different one; that is an error, so a mistyped filter cannot
  silently rewrite the wrong document.
- **A delete filter is required**, unlike `find` and `count` where omitting it
  means "everything". Delete has no undo, so emptying a collection must not be
  the easiest thing to type.

---

## Go

```go
db, err := nosqlite.Open("./demo.nsq")
defer db.Close()

users, _ := db.Collection("users")
id, _ := users.Insert(map[string]any{"name": "Ada", "age": 36, "tags": []any{"math"}})

docs, _ := users.Find(nosqlite.Query{
    Filter:     map[string]any{"age": map[string]any{"$gte": 30}},
    Projection: map[string]any{"name": 1, "_id": 0},
    Sort:       []nosqlite.SortKey{{Field: "age", Desc: true}},
    Limit:      10,
})
```

```go
type Query struct {
    Filter     map[string]any // nil or empty matches everything
    Projection map[string]any // nil or empty returns whole documents
    Sort       []SortKey      // applied in order; empty means insertion order
    Skip       int
    Limit      int            // 0 means no limit
}

type SortKey struct {
    Field string   // dotted path
    Desc  bool
}
```

The rest of the surface:

```go
func Open(path string, opts ...Option) (*DB, error)
func (db *DB) Close() error
func (db *DB) Collection(name string) (*Collection, error)   // creates on demand
func (db *DB) Collections() []string
func (db *DB) Path() string
func (db *DB) Size() int64
func (db *DB) DeadBytes() int64      // bytes held by superseded records
func (db *DB) Sync() error

func (c *Collection) Insert(doc map[string]any) (string, error)
func (c *Collection) InsertJSON(raw []byte) (string, error)
func (c *Collection) InsertMany(docs []map[string]any) ([]string, error)
func (c *Collection) Replace(filter, doc map[string]any) (int, error)   // at most 1
func (c *Collection) Delete(filter map[string]any) (int, error)         // at most 1
func (c *Collection) DeleteMany(filter map[string]any) (int, error)     // all matches
func (c *Collection) Find(q Query) ([]map[string]any, error)
func (c *Collection) FindOne(filter map[string]any) (map[string]any, error)  // nil if none
func (c *Collection) Count(filter map[string]any) (int, error)
func (c *Collection) ForEach(q Query, fn func(doc map[string]any) error) error
func (c *Collection) Name() string
func (c *Collection) Len() int       // from the index, no I/O

func ScanLive(path string) (LiveStats, error)   // live/dead accounting, offline
```

**`Find` versus `ForEach`.** `Find` materialises its results, so a filter
matching a million documents returns a million documents no matter how frugal
the engine was. `ForEach` retains nothing between callbacks — reach for it over
a large result set, and return an error (or `ErrStop`) to halt the scan.

**Numbers are `float64`.** JSON has no integer type, so `42` round-trips as
`float64(42)`:

```go
age := doc["age"].(float64)   // not int
```

Only Go sees this: Python's `json.loads` hands back an `int`, and every
TypeScript number is a double anyway.

### Options

```go
nosqlite.Open(path,
    nosqlite.WithSync(nosqlite.SyncNever),      // fsync on Close only; default is every write
    nosqlite.WithFastOpen(),                    // skip CRC verification during replay
    nosqlite.WithTrace(nosqlite.TraceAll),      // off | writes | all | verbose
    nosqlite.WithTraceFile("/tmp/x.trace"),     // default <dbpath>.trace
    nosqlite.WithTraceAppend(),                 // keep history instead of truncating
    nosqlite.WithTraceMaxBytes(64<<20),         // default 64 MB
)
```

Functional options, so adding a mode never breaks the `Open` signature. The
bindings expose the same set as keyword arguments.

---

## Python

```sh
make build      # python/nosqlite/libnosqlite.so — required before any import
```

```python
from nosqlite import Database

with Database("./demo.nsq", trace="all") as db:
    users = db["users"]

    users.insert({"name": "Ada", "age": 36, "tags": ["math"]})
    users.insert_many([{"name": "Grace", "age": 45}, {"name": "Alan", "age": 41}])

    for u in users.find({"age": {"$gte": 40}}, sort=[("age", -1)], limit=10):
        print(u["name"], u["age"])

    users.find_one({"name": "Ada"})     # dict, or None
    users.count({"age": {"$gte": 40}})  # 2
    len(users)                          # 3
    db.collections()                    # ['users']
```

Keyword arguments: `find(filter, *, projection, sort, skip, limit)`,
`Database(path, *, sync, trace, trace_file, fast_open)`. Sort keys are
`[("age", -1)]` pairs. Errors raise `NoSQLiteError`.

**`find(filter, limit=…)`** caps how many documents are returned.

| `limit` | returns |
|---|---|
| omitted | at most 1000 — the default |
| `N` | at most `N` |
| `0` | every match |

If no limit is given, and there are 1000 or more matches, `find()` raises
`NoSQLiteError`.

`iter_find()` streams instead, with flat memory and no cap.

The package is a thin `ctypes` wrapper over the shared library and imports
straight out of the checkout — nothing is installed into your system Python.

---

## TypeScript

```sh
make build      # the shared library — required first
```

```ts
import { Database } from "../../typescript/nosqlite/index.ts";

const db = new Database("./demo.nsq", { trace: "all" });
try {
  const users = db.collection("users");

  users.insert({ name: "Ada", age: 36, tags: ["math"] });
  users.insertMany([{ name: "Grace", age: 45 }, { name: "Alan", age: 41 }]);

  for (const u of users.find({ age: { $gte: 40 } }, { sort: [["age", -1]], limit: 10 })) {
    console.log(u.name, u.age);
  }

  users.findOne({ name: "Ada" });      // object, or null
  users.count({ age: { $gte: 40 } });  // 2
  db.collections();                    // ["users"]
} finally {
  db.close();
}
```

Options object:
- `find(filter, { projection, sort, skip, limit })`
- `new Database(path, { sync, trace, traceFile, fastOpen })`
- Sort keys are `[["age", -1]]` tuples.
- Errors throw `NoSQLiteError`.

**`find(filter, { limit })`** caps how many documents are returned, exactly as in
Python: omitted means at most 1000, `N` means at most `N`, `0` means every match.
If no limit is given, and there are 1000 or more matches, `find()` throws
`NoSQLiteError`.

`iterFind()` streams instead, with flat memory and no cap.

**A document's field values are typed `unknown`.** `Document` is
`Record<string, unknown>`, because the store has no schema — nothing at compile
time can know that `age` holds a number. Reading a field is fine; *using* it as
a number or a string needs a conversion first:

```ts
for (const u of users.find({ age: { $gte: 40 } })) {
  console.log(u.name);              // fine — console.log accepts anything
  // u.age + 1                      ✗ error TS18046: 'u.age' is of type 'unknown'
  const older = Number(u.age) + 1;  // ✓
}
```

**No build step.** Node ≥ 22.18 strips the types and runs the `.ts` source
directly; `make ts-check` runs `tsc` when you want them actually checked. Node
has no built-in FFI, so the binding loads the library with
[`koffi`](https://koffi.dev) — its one dependency, prebuilt, no compiler needed.

---

## See also

- [`filters.md`](filters.md) — what goes inside a filter or a projection
- [`bindings.md`](bindings.md) — the C ABI underneath Python and TypeScript
- [`trace-and-cli.md`](trace-and-cli.md) — seeing what a call actually did
- [`design.md`](design.md) — why the API has this shape
