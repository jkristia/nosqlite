# Testing

One engine (Go) and two thin FFI wrappers, so a correctness question only needs
answering **once** if all three languages run it against the same input and the
same expected output. That is what conformance tests are, and it drives the whole
layout.

---

## Where does my test go?

| I am testing | write | where |
|---|---|---|
| one function, one language | a unit test | next to the source |
| a filter operator, a query behaviour, "do all bindings agree" | a conformance case | `conformance/testdata/cases/` |
| a write path (replace, delete, re-insert) | a conformance case with `mutations` | `conformance/testdata/cases/mutate/<op>/` |
| a large or adversarial dataset, or a perf budget | a scale test | `scale/<lang>/` |

```sh
make test              # Go unit tests
make test-race         # the concurrency tests are worth running under -race
make conformance       # Go conformance suite
make conformance-py    # Python — creates .venv on first run
make conformance-ts    # TypeScript — jest
make all               # everything, plus fmt, vet, build, cli
```

| kind | answers | lives | data |
|---|---|---|---|
| **Unit** | does this function behave? | next to the source, per language | inline, or a per-package `testdata/` |
| **Conformance** | does every binding agree? | `conformance/<lang>/` | shared, in `conformance/testdata/` |
| **Scale** | does it hold up at a million documents? | `scale/<lang>/` | its own, in `scale/testdata/` |

Each kind owns its data. Conformance data is small and representative; scale data
is large and maybe generated — separate directories avoid the false suggestion
that they are interchangeable.

---

## Unit tests

Go convention: `store_test.go` beside `store.go`, same package, so it can reach
unexported symbols. Keep them fast and hermetic. A fixture goes in a `testdata/`
folder next to the package that uses it.

Python and TypeScript follow their own ecosystem's convention
(`python/tests/`, `typescript/nosqlite/*.test.ts`) if those wrappers ever grow
enough logic to warrant it — a wrapper this thin may never need one.

---

## Conformance fixtures

```
conformance/testdata/
    query.schema.json         # shape of query.json
    expected.schema.json      # shape of expected.json
    datasets/<name>.jsonl     # documents to insert, one per line — reused by many cases
    cases/<case-name>/
        query.json            # {"dataset", "mutations", "filter", "projection", "sort", "skip", "limit"}
        expected.json         # what every binding must return
```

**A case names its dataset rather than embedding it**, so the same "1,000 users,
mixed types" dataset serves dozens of cases and gets edited in one place.

**A case is a sequence:** seed the dataset, apply `mutations` in order, run the
query. The query is how a write case makes its assertion — there is no separate
"expected database state" file, and `expected.json` keeps its one meaning.

```json
"mutations": [
  {"op": "replace", "filter": {"_id": "9"}, "document": {"name": "..."}, "matched": 1}
]
```

| key | means |
|---|---|
| `op` | `insert`, `replace`, `delete` or `delete_many` |
| `matched` | optional assertion on the returned count, checked before the query runs |
| `error` | assert the write must **fail**, matched as a substring |

`matched` and `error` are mutually exclusive — a failed operation returns no
count. Error text is matched as a substring because only the message is genuinely
shared: Go returns an `error`, Python raises, TypeScript throws, and pinning the
exact rendering would test three error conventions instead of one rule.

A rejected write's case then queries the unchanged data, which is how it proves
the refused write wrote *nothing*.

`insert` exists as a mutation so a case can add a document *after* seeding, which
is the only way to express delete-then-re-insert-the-same-`_id`. Omitting
`mutations` gives a plain read-only case. **An `op` a runner doesn't implement is
a hard failure, never a skip** — a fixture that silently tested nothing would be
worse than no fixture.

**Asserting on documents.** `expected.json` always carries `ids`, the `_id` list
`Find` must return in order. Beyond that:

- With `fields`, the runner narrows the returned documents to those names itself
  — client-side and flat — and compares that against `docs`. For cases that query
  whole documents but only want a few fields spelled out.
- Without `fields`, `docs` is compared **in full**. That is the mode a case with
  a `projection` uses, since the shape the engine produced is the point.

A projection that drops `_id` is the one case that may omit `ids`.

**`$schema` as the first key.** These are hand-authored JSON with no compiler
behind them, and `filter` in particular is the [`filters.md`](filters.md) grammar
rather than anything guessable from the field name. Declaring `"$schema"`
(a relative path, one `../` per level below `cases/`) gives editors with a JSON
language service real autocomplete, hover docs and validation — the closest thing
to a typed interface a JSON fixture can have. The readers ignore it.

---

## Conformance runners

```
conformance/go/          package nosqlite_test — public API only, black-box
conformance/python/      pytest
conformance/typescript/  jest, its own npm project
```

Each suite is **one generic runner**, not one file per case: it walks
`../testdata/cases/` recursively and treats any directory containing a
`query.json` as a case, at any depth. There is deliberately no
`conformance/go/age-gte` — a new case should cost one fixture folder and zero
lines of code, or every case would mean writing a test three times.

The correspondence is by **name**: `cases/filter/comparison/age-gte` becomes
`TestConformance/filter/comparison/age-gte`, so
`go test -tags=conformance -run TestConformance/age-gte ./conformance/go` runs
one case. Python and TypeScript parametrize on the same relative path.

The Go suite uses the external test package (`nosqlite_test`) so it exercises
exactly what the other two exercise — the public API — rather than reaching into
internals. It is behind a build tag so the fast unit loop is unaffected:

```go
//go:build conformance
```

### The TypeScript suite

Its own `package.json` and `node_modules`, separate from `typescript/`'s, because
it needs `jest` and `ts-jest` which the shipped binding has no reason to depend
on.

**`ts-jest`, not Node's type-stripping.** Jest 30 advertises native stripping,
but its ESM loader (`--experimental-vm-modules`) doesn't run through the code
path Node patches for it, so a `.ts` test file fails with a plain syntax error.
An open limitation, not a config mistake.

**One explicit `test(...)` per case, written by hand** — the one deliberate
divergence from Go's zero-lines-per-case rule. The reason is debugging: a
dynamically generated test can't be found by text search or landed on with a
breakpoint. The cost is one boilerplate line per case:

```ts
test("some-new-case", () => {
  const { got, expected } = cases.run("some-new-case");
  expect(got).toEqual(expected);
});
```

Everything that isn't the test itself — reading the fixtures, opening a temp
database, converting `{field, desc}` into `SortKey` tuples — is on one
`CaseRunner` class in `case-runner.ts`. `cases.run()` returns `{got, expected}`
so the assertion stays in the test body, where a stopped debugger can inspect
both. The fixture-format types live there too, not in the binding's public API:
they describe a *file format*, which is a testing concern.

**In VS Code**, `.vscode/settings.json` sets `jest.rootPath` and the
`--experimental-vm-modules` command line, which is enough for Test Explorer to
discover, run and debug every case.

---

## Scale tests

Same "one fixture, every language" idea, different question: does a large dataset
still behave correctly, and within acceptable time and memory? These assert on
timing, memory and the absence of corruption more than on exact output — mixing
them with conformance would make a slow test noisy for correctness review and a
strict output diff noisy for a perf budget.

**Measure in the language the database is used from.** The number that matters is
what a caller experiences end to end, through the FFI boundary and the JSON
marshalling. A Go figure of 0.1 ms means nothing to a caller waiting 5 s, so the
consuming language sets the budget and Go benchmarks are the diagnostic layer
that explains a blown one.
