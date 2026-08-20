# Delete — bindings and conformance

`docs/todo.md` §1. Carries the already-finished Go delete engine across the FFI
boundary and into the shared conformance corpus.

## Context

The engine side of delete is done and committed: `Collection.Delete` /
`DeleteMany` (collection.go:309-430) write `op=2` tombstones, `removeIndex`
compacts the slot array and remaps the `_id` table, and replay reconstructs the
result by mark-then-compact. What does not exist is any way to reach it from
TypeScript or Python, and no conformance case exercises it — so the three
bindings are currently *known* to disagree, in that two of them cannot delete at
all.

This is the same two-commit shape `Replace` landed in (e16fb67): engine first,
then the C ABI, both bindings, and the shared corpus. Nothing here changes the
file format or the engine; it is a boundary-crossing exercise plus fixtures.

Two decisions, settled before writing code:

- **`delete` / `deleteMany` take a required filter** in both bindings. `count()`
  and `find()` default an omitted filter to "everything", but delete has no undo
  — `users.deleteMany()` must not be the easiest thing to type. Go already
  forces an explicit `nil`.
- **The conformance `insert` op ships in this batch.** It is ~3 lines per runner
  and it is the only way to express §6.5 of
  [`replace-delete-and-compaction.md`](../docs/replace-delete-and-compaction.md)
  — delete, then re-insert the same `_id` — across bindings. Today a case can
  only insert through its dataset.

## 1. C ABI — `capi/capi.go`

Two exports, both shaped exactly like `nsq_count` (capi/capi.go:390), which
already takes `(handle, coll, filterJSON)` and parses an optional filter:

```go
//export nsq_delete
func nsq_delete(id C.longlong, coll *C.char, filterJSON *C.char) (out *C.char)
                                          // → {"deleted":1} | {"error":"..."}
//export nsq_delete_many
func nsq_delete_many(id C.longlong, coll *C.char, filterJSON *C.char) (out *C.char)
                                          // → {"deleted":n} | {"error":"...","deleted":n}
```

Both follow the `defer guard(&out)` / `acquire` / `defer release` preamble every
export uses. The one asymmetry: `nsq_delete_many` reports its count *alongside*
the error, the way `nsq_insert_many` reports its ids (capi/capi.go:287-292).
`DeleteMany` returns how many tombstones actually landed, and on a partial batch
that number is the one to trust — dropping it would lose information the engine
went out of its way to preserve.

## 2. TypeScript — `typescript/nosqlite/`

- `ffi.ts`: two `lib.func(...)` declarations next to `nsqCount` (ffi.ts:102),
  and two thin wrappers returning `Number(reply.deleted)`, modelled on
  `count()`/`replace()`.
- `index.ts`: `delete(filter: Filter): number` and
  `deleteMany(filter: Filter): number` on `Collection`, in the `-- writing --`
  block after `replace`. `delete` is a reserved word as a bare identifier but
  legal as a method name; it does not need renaming.
- Doc comments carry the two things a caller must know and cannot infer: the
  file gets **bigger** (tombstone appended, old record abandoned, both reclaimed
  only by `Compact`), and `deleteMany` is not atomic — the returned count is
  what actually landed. Note that `call()` in ffi.ts raises on `error` and drops
  the partial count, exactly as it already does for `insertMany`.

## 3. Python — `python/nosqlite/`

Same two steps, same order: `_lib.py` argtypes/restype beside `nsq_count`
(_lib.py:98) plus `delete` / `delete_many` wrappers, then `Collection.delete` /
`Collection.delete_many` in `__init__.py` after `replace` (__init__.py:155),
with docstrings mirroring the TypeScript ones.

## 4. Conformance

**Schema** — `conformance/testdata/query.schema.json`: extend the mutation `op`
enum from `["replace"]` to `["replace", "delete", "delete_many", "insert"]` and
extend the `document` / `matched` descriptions to say what each op means
(`document` is required for `replace`/`insert` and unused by the deletes;
`matched` is the delete count, and 1 for a successful insert).

**Runners** — each currently hardcodes `if op != "replace"` immediately before
calling `Replace`, deliberately outside the error-assertion path so an
unimplemented op can never be mistaken for the failure an `error` assertion
expects. That property must survive: turn the guard into a dispatch that
computes the count, keeping the unknown-op check ahead of the call.

- `conformance/go/conformance_test.go:166` — `applyMutation`
- `conformance/typescript/case-runner.ts:142` — `CaseRunner.applyMutation`
- `conformance/python/case_runner.py:64` — `_apply_mutation`

For `insert`, the count is 1 on success, so the existing `matched` / `error`
machinery needs no change at all.

**Cases** — `conformance/testdata/cases/mutate/delete/`, all against the
`people` dataset (53 documents, `_id` "1".."53"). Two rules learned from
replace: a no-op case needs `matched` or `error` to carry the assertion, since
the query alone cannot fail; and assert insertion **order** afterwards, not just
membership, because order is precisely the property delete does not preserve.

| case | asserts |
| --- | --- |
| `delete-removes-document` | `matched: 1`; survivors keep insertion order with the hole closed |
| `delete-no-match-is-noop` | `matched: 0`; nothing written, collection unchanged |
| `delete-only-first-match` | a filter matching many removes exactly the first, the rest survive in order |
| `delete-many-removes-all-matches` | `matched: n`; every match gone, survivors still in order |
| `delete-many-empty-filter-empties-collection` | `matched: 53`, query returns `[]` |
| `delete-frees-id` | delete `_id` "3", re-insert it — succeeds, and lands at the **end** of insertion order, not back at position 3 (§6.5; the Go analogue is delete_test.go:296) |
| `insert-duplicate-id-rejected` | the same insert *without* the delete fails; `error` asserts `ErrDuplicateID`'s text. The control that makes the case above mean something |

**TypeScript test blocks** — Go and Python discover cases by walking the tree;
`conformance/typescript/conformance.test.ts` writes one explicit `test(...)` per
case on purpose (docs/testing.md §4a), so each new case needs a block appended.

## 5. Docs

- `docs/design.md`: line 6 ("Supported today: insert, query and replace") gains
  delete; §8's ABI listing gains the two exports; §9's Python-wrapper bullet
  list gains `delete` / `delete_many` beside the `replace` bullet (design.md:909).
- `docs/testing.md` §3: the `mutations` prose names `replace` as the only op —
  extend the op list.
- `docs/todo.md`: delete §1 outright once it lands. That file says what is left,
  not what was done.

## Verification

```
make build            # regenerates libnosqlite.{dylib,h}; both bindings load it
make test             # Go unit tests, unchanged
make conformance      # Go runner over the new cases
make conformance-ts   # TypeScript runner, same fixtures
make conformance-py   # Python runner, same fixtures
make ts-check         # tsc over the binding — the only step that reads the types
```

`make all` runs fmt, vet, test, build, cli and all three suites; that is the
real gate. The three conformance suites agreeing on the new `mutate/delete/`
cases is the actual acceptance criterion — a case that passes in Go and fails in
Python is exactly the disagreement this suite exists to catch.

Worth checking by hand once, since no automated case covers it: `nsq stat` on a
file that has been deleted from should report the tombstones as dead bytes and
still suggest `nsq compact` (the verb that does not exist yet — todo §2).
