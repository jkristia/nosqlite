# nosqlite — testing

How tests are organized, and where a new test should go.

---

## 1. Three kinds of test

nosqlite has one engine (Go) and two thin FFI wrappers (Python, TypeScript) that call
the same compiled C API in [`capi/`](../capi/). That shape drives the test layout: a
correctness question ("does `$gte` match this document?") only needs to be answered
once per language *if* all three languages run it against the same input and check it
against the same expected output. That's a **conformance** test. It's distinct from a
**unit** test (one function, one file, one language) and from a **scale** test (does
it still work — and work fast enough — at a million documents?).

| Kind | Answers | Lives | Data |
|---|---|---|---|
| Unit | Does this function behave correctly? | Next to the source, per language | Inline, or a per-package `testdata/` |
| Conformance | Does every binding agree on this behavior? | `conformance/<lang>/` | Shared across the 3 languages, in `conformance/testdata/` |
| Scale | Does it hold up on large/adversarial datasets? | `scale/<lang>/` | Its own, in `scale/testdata/` |

Each kind owns its data, colocated with the tests that consume it — there's no single
repo-wide `testdata/` shared by everything. Conformance and scale want different
data by nature (conformance: small, representative, one case per behavior; scale:
large, maybe generated, maybe not worth committing to git at all), so giving them
separate directories avoids the false suggestion that they're interchangeable.

---

## 2. Unit tests — next to the source

Go convention, and what this repo already does: a `_test.go` file sits beside the
file it tests, in the same package, so it can reach unexported symbols.

```
query.go
query_test.go
store.go
store_test.go
internal/engine/compare.go
internal/engine/compare_test.go
```

Run with the normal `go test ./...`. These should stay fast and hermetic — no large
fixtures, no cross-language concerns. If a unit test needs a fixture file, use the
standard Go idiom of a `testdata/` folder next to it — e.g.
`internal/engine/testdata/` for a matcher test that wants a JSON document on disk
instead of inline. Scoped to that package, not shared with anything else. Python
and TypeScript follow their own ecosystem's unit-test convention when those
wrappers grow enough logic to warrant it (`python/tests/`,
`typescript/nosqlite/*.test.ts`) — a wrapper this thin may never need one.

---

## 3. Conformance fixtures — `conformance/testdata/`

A dataset (the documents to insert) and a case (one query against a dataset, plus
its expected result) don't share a lifecycle — the same "1,000 users, mixed
types" dataset is worth reusing across dozens of filter/sort/skip/limit cases, and
duplicating it into every case folder would mean updating N copies in lockstep
whenever it changes. So they're split:

```
conformance/
    testdata/
        query.schema.json        # shape of query.json — see below
        expected.schema.json     # shape of expected.json
        datasets/
            <dataset-name>.jsonl     # documents to insert, one per line — reused by many cases
        cases/
            <case-name>/
                query.json           # {"dataset": "<dataset-name>", "filter": ..., "sort": ..., "skip": ..., "limit": ...}
                expected.json        # the result every binding must produce for that query
```

A case names its dataset rather than embedding it, so `query.json` stays small and
readable, and a dataset can gain new cases without being copy-pasted.

`query.json` and `expected.json` are hand-authored JSON with no compiler behind
them, which makes "what am I allowed to put in here" a real question — `filter` in
particular is the Mongo-dialect grammar from [`matcher.md`](matcher.md), not
something guessable from the field name alone. Rather than inventing a build step,
each file declares `"$schema": "../../query.schema.json"` (or `expected.schema.json`)
as its first key. Editors with a JSON language service (VS Code out of the box) read
that and give real autocomplete, hover docs, and validation — the closest thing to a
typed interface JSON fixtures can have. `$schema` is ignored by the Go/Python/TS
readers; it's an editor hint, not part of the data.

It lives under `conformance/` rather than at the repo root because it belongs to
conformance specifically — scale tests want different data entirely (§5), and a
root-level `testdata/` would wrongly suggest the two share a pool. Go still leaves
it alone either way (`testdata/` is a build-tool-ignored name at any depth); Python
and TypeScript reach it by relative path from `conformance/<lang>/`.

Keeping fixtures out of each language's test tree is the point: a new filter
operator or edge case gets one case, and three test runs (Go, Python, TypeScript)
either all pass or the disagreement itself is the bug report.

---

## 4. Conformance tests — `conformance/<lang>/`

```
conformance/
    testdata/
    go/            # package nosqlite_test — public API only, black-box
    python/        # pytest
    typescript/    # jest — see §4a
```

Each suite is **one generic runner**, not one file per case: it walks
`../testdata/cases/` recursively, and treats any directory containing a
`query.json` as a case — at any depth. For each one it inserts that case's
`dataset` (from `../testdata/datasets/`) through the language's public API, runs
`query.json`, and diffs the result against `expected.json`. `conformance/go/` does
not mirror `testdata/cases/<name>/` on disk — there is deliberately no
`conformance/go/age-gte`. If there were, every new case would mean writing a new
test in Go *and* Python *and* TypeScript, which is exactly the duplication the
shared-fixture split in §3 exists to avoid; a new case should cost one fixture
folder and zero lines of code in any language.

The correspondence that does exist is by **name**, not by path: Go's
`t.Run(caseName, ...)` turns the case's path *relative to `cases/`* into the
subtest name, so `cases/age-gte` shows up as `TestConformance/age-gte`, and
`cases/filter/comparison/age-gte` would show up as
`TestConformance/filter/comparison/age-gte`. Either can be run on its own:
`go test -tags=conformance -run TestConformance/age-gte ./conformance/go`. Python
and TypeScript should do the same — parametrize over each case's path relative to
`cases/` so `pytest -k age-gte` / `-t age-gte` finds the same one.

With one case this flat: `cases/age-gte/` is fine. Once there are dozens, group
them into subdirectories by whatever axis makes them findable — operator
(`cases/filter/comparison/`, `cases/filter/logical/`), feature
(`cases/sort/`, `cases/skip-limit/`) — the runner doesn't care, it only cares that
`query.json` exists somewhere under `cases/`.

The Go suite uses the external test package (`nosqlite_test`, not `nosqlite`) so it
exercises exactly what Python and TypeScript exercise — the public API, through the
C boundary where relevant — rather than reaching into internals the way the unit
tests do.

Gate it behind a build tag so the fast unit-test loop is unaffected:

```go
//go:build conformance

package nosqlite_test
```

```
go test -tags=conformance ./conformance/...
```

---

## 4a. The TypeScript conformance suite

`conformance/typescript/` is its own small npm project — its own `package.json`
and `node_modules`, separate from `typescript/`'s — because it needs `jest` and
`ts-jest`, which the shipped binding has no reason to depend on. `make
conformance-ts` builds the library and runs it; under the hood that's `npm
--prefix conformance/typescript test`.

**Why `ts-jest`, not plain Node type-stripping.** The rest of the TypeScript
side deliberately has no build step — Node ≥ 22.18 strips `.ts` type
annotations at load time, so `node examples/basic/basic.ts` just runs. The
natural instinct was to keep that here too: Jest 30 advertises native
type-stripping support with no transformer. In practice it doesn't work —
Jest's own ESM module loader (`--experimental-vm-modules`) doesn't run
through the code path Node patches for stripping, so a stripped-nothing
`.ts` test file fails with a plain syntax error. This is a real, currently
open limitation, not a config mistake (confirmed by hand: same failure with
or without `--experimental-strip-types`). `ts-jest` in ESM mode
(`ts-jest/presets/default-esm`-equivalent config, `useESM: true`) is the
proven fallback — same `import`/`export` source, just transformed by
`ts-jest` instead of Node.

**No dynamic test generation — one `test(...)` per case, written by hand.**
The Go suite discovers cases at runtime (`WalkDir` over `testdata/cases/`,
§4) so that adding a case costs zero lines of code. The TypeScript suite
deliberately does *not* do this: `conformance.test.ts` has one explicit,
named `test("age-gte", ...)` block per case. The reason is debugging, not
principle — a dynamically-generated test can only be reached by running the
whole discovery loop and picking out the right iteration; there is no
`age-gte` you can find with a text search or land a breakpoint directly on.
An explicit `test(...)` is exactly as findable and breakpointable as any
other unit test, which is the property that matters here. The cost is one
line of near-identical boilerplate per new case:

```ts
test("some-new-case", () => {
  const { got, expected } = cases.run("some-new-case");
  expect(got).toEqual(expected);
});
```

That's an intentional divergence from Go's "zero lines of code per case"
rule, scoped to TypeScript for now. If Go's dynamic `t.Run` subtests turn out
to have the same debugging friction in practice, apply the same fix there —
nothing about the fixture format changes either way.

**Where the fixture-loading logic lives — `conformance/typescript/case-runner.ts`.**
Everything that isn't the test itself — reading `query.json`/`expected.json`,
looking up the named dataset, opening a temp database, converting the
fixture's `{field, desc}` sort keys into nosqlite's `SortKey` tuples — is a
method on one class, `CaseRunner`. `conformance.test.ts` constructs a single
`CaseRunner` and each test calls `cases.run("case-name")`, which returns
`{ got, expected }` for the test to `expect(...)` against — the assertion
itself stays in the test body, not buried in the class, so a debugger
stopped on it can inspect both lists directly. `CaseQuery` / `CaseExpected`
(the fixture-format types) are declared in the same file, not added to
`typescript/nosqlite/`'s public API — they describe the *fixture file
format*, which is a testing concern, not something a library consumer needs.
This mirrors the Go side, where `caseQuery`/`caseExpected` live in
`conformance/go/conformance_test.go` rather than in the `nosqlite` package.

**Running it from VS Code.** `.vscode/settings.json` sets
`jest.rootPath: "conformance/typescript"` (so the extension finds this
project's `jest.config.js`, separate from any future `typescript/` tests) and
`jest.jestCommandLine: "node --experimental-vm-modules node_modules/.bin/jest"`
(the flag Jest's ESM support needs, otherwise unrelated to the type-stripping
question above). With the official `vscode-jest` extension installed, that's
enough for Test Explorer to discover, run, and debug every case.

---

## 5. Scale tests — `scale/<lang>/`

Same "one fixture, run through every language" idea, but the question and the data
are both different from conformance: does a large or adversarial dataset still
behave correctly, and within acceptable time/memory? These assert on timing,
memory, and absence of panics/corruption more than on exact output — which is why
they're a separate tree from `conformance/` rather than just "conformance with
bigger fixtures." Mixing the two makes a slow scale test noisy for correctness
review, and a strict output-diff noisy for a perf budget.

```
scale/
    testdata/      # large, maybe generated — decide per-dataset whether to commit
                    # it to git or generate it on demand (a `//go:generate` script,
                    # or a fetch step) once one is actually big enough to matter
    go/
```

Add `python/` and `typescript/` here the same way conformance grows: when there's
an actual second suite to port, not preemptively.

---

## 6. Adding a new test

- **Bug in one function, one language** → unit test next to the source, fixture in
  a per-package `testdata/` if needed.
- **New filter operator, new query behavior, "does every binding agree"** → a case
  in `conformance/testdata/`, run through `conformance/go`, `conformance/python`,
  `conformance/typescript`.
- **Large dataset, performance, or stress scenario** → `scale/go` (and the other
  languages once they exist), data in `scale/testdata/`.
