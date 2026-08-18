# nosqlite — todo

The working list: what to build next, in order, with the decisions that have to be
made before each one can start.

**Keep it live.** Tick boxes as they land, and delete a section outright once its
item is finished — this file says what is left to do, not what was done. Notes that
outlive the task belong in the design docs instead.

This is the *task* view. [`design.md`](design.md) §11 is the *architectural* view —
the same work seen as extension points in the design, and the place to look for
where a feature is meant to plug in. §11 is ordered by design dependency; this file
is ordered by what to actually do next. Where they disagree about ordering, this
file wins; where they disagree about mechanism, §11 does.

---

## 0. Make `replace-delete-and-compaction.md` readable

**Tomorrow's first job.** The document is correct but does not explain — reading it
does not leave you able to say back how delete and its replay work. Fix the
explanation, not the facts.

- [ ] Have the reader mark the exact paragraphs that lost them, and start there.
      Everything below is a guess at the cause until that exists.
- [ ] Lead with the worked example. §6's "three inserts and one delete" is the
      clearest thing in the file and it sits at line ~400, behind three sections of
      invariants. The trace should come first and the rules should be read off it.
- [ ] Split §6.4. One section currently carries four separate ideas: the two-phase
      replay, why the idTable entry cannot be removed, why zero length is the marker,
      and what an unresolvable tombstone means. Each deserves its own heading.
- [ ] Cut the justification down. Long stretches argue against designs that were
      never chosen (free lists, live flags, in-place deletion). Keep one sentence of
      "not this, because", move the rest out of the reader's path.
- [ ] Consider splitting the file. Replace, delete, and compaction are three
      subjects, 845 lines, 26 headings; the terms table exists because the reader is
      expected to hold all three at once.

---

## 1. Delete — bindings and conformance

The Go engine is done: `Delete`/`DeleteMany`, and replay of the `op=2` records they
write. What is left is carrying it across the boundary, which is how `Replace`
landed too — the engine in one commit, the bindings and the shared corpus in the
next.

- [ ] `nsq_delete` / `nsq_delete_many` in `capi/capi.go`, replying `{"deleted": n}`.
      Follow `nsq_count`'s shape, which already takes exactly `(handle, coll,
      filterJSON)`. `nsq_delete_many` should report its count alongside the error on
      a partial batch, as `nsq_insert_many` does — `DeleteMany` returns how many
      tombstones actually landed, and that number is the one to trust when the call
      failed.
- [ ] TypeScript: `ffi.ts` declarations and wrappers, then `delete` / `deleteMany`
      on `Collection`. `delete` is a reserved word as a bare identifier but legal as
      a method name — it does not need renaming to `remove`.
- [ ] Python: `_lib.py` argtypes and wrappers, then `delete` / `delete_many`.
- [ ] Conformance cases in `testdata/cases/mutate/delete/`.

**Conformance is already ready for it.** The op names are settled: `"delete"` and
`"delete_many"`, added to `query.schema.json`'s `op` enum, with one arm in each of
the three runners' `applyMutation`. Sequencing, `matched` and `error` all work
unchanged; `document` is already optional in the schema, so a delete mutation
validates as soon as the enum admits it. Two things to get right, both learned from
replace:

- a case shaped as a no-op needs its `matched` or `error` to carry the assertion —
  the query alone cannot fail
- assert insertion **order** afterwards, not just membership, since that is precisely
  the property delete does not preserve

**Worth adding while in there:** an `"insert"` op. §6.5 of
[`replace-delete-and-compaction.md`](replace-delete-and-compaction.md) — delete, then
re-insert the same `_id` — is the sharpest delete behaviour there is, and it cannot be expressed in
the corpus today because a case can only insert through its dataset. It is about
three lines per runner.

---

## 2. Compaction

Step 8. Not optional now that delete has landed: `nsq stat` already prints "consider
`nsq compact <path>`" for a verb that does not exist yet, and a delete makes the file
bigger until it does.

- [ ] `Compact()` rewrites the file keeping only live versions, regrouped by
      collection so scan locality is restored.
- [ ] `nsq compact` CLI verb.
- [ ] `DropCollection`, which falls out of the same machinery.

No format change. Compaction rewrites every record, so **every offset changes**.
Anything holding an offset — snapshots, and later any index — must be rebuilt or
remapped across it.

---

## 3. Projections

§11 item 3: `Query.Project`, applied during the copy-out. Wanted as a feature —
retrieve a subset of fields rather than whole documents — and it is also the
cheapest win available on the binding-side cost that item 6 is about to measure.

- [ ] `Query.Project` in the engine, applied at copy-out.
- [ ] Carry it across the C ABI in `wireQuery`, and through both bindings.
- [ ] Conformance cases.

**Where the win actually lands.** Projection shrinks the C string, the
`JSON.parse` / `json.loads` on the far side, and all three result copies. It does
*not* avoid the full `json.Unmarshal` in Go — the engine still parses the whole
document to pick fields out of it. That is partial parsing (§11 item 4), and the two
compose: partial parsing alone only helps documents that *don't* match, since a
match has to be returned whole. With a projection, `RequiredPaths()` becomes
filter-fields ∪ projected-fields and a matching document never needs a full decode
either. Projection first makes partial parsing worth far more.

Settle before writing code:

- [ ] **Inclusion or exclusion syntax.** Mongo's `{field: 1}` / `{field: 0}`, which
      cannot be mixed in one projection — except `_id`, which is included by default
      and must be excluded explicitly. Follow it, per the repo's "Mongo's query
      language, Go's API shape" rule.
- [ ] **Dotted paths** — `{"address.city": 1}` has to rebuild a partial subdocument,
      not flatten the key.

Array projection operators (`$slice`, positional `$`) are out of scope; note them as
unsupported rather than half-implementing them.

**The conformance harness already has the shape.** `expected.schema.json`'s `fields`
key exists precisely because there is no projection — the runners take the full
documents `Find()` returns and narrow them client-side. Once this lands, those cases
can move to a real `project` in `query.json` and the client-side narrowing becomes
the fallback for cases that deliberately check whole documents.

---

## 4. npm packaging (local install)

Goal: `npm install` this into a real Node project from a local path, not the
registry. Cheap, and it is the forcing function that shows what is actually missing
from the binding.

- [ ] Move `koffi` to real `dependencies`.
- [ ] Decide: ship raw `.ts` (works via Node ≥ 22.18 type-stripping, needs an
      `engines` field) or compiled `.js` + `.d.ts` (safer for a consumer that is not
      on 22.18).
- [ ] `files` / `exports` / `types` in `package.json`.
- [ ] The platform-specific `.dylib` / `.so` / `.dll`: either commit one platform's
      binary, or a `postinstall` that runs `go build` (which requires Go on the
      machine).
- [ ] Verify with `npm install file:../nosqlite/typescript` from a scratch project.

---

## 5. Secondary indexes

§11 item 2: a planner walking the Matcher tree to turn `cmpNode` / `inNode` on an
indexed field into a lookup, with the rest as a residual filter.

**Already decided by the design:** indexes rebuild from the log on open — nothing new
on disk, so no new op code and no `formatVersion` question. The existing lazy
`idTable` is the precedent to generalise: it is already a secondary index on `_id`,
built on first need, and `removeIndex`'s remap is the precedent for what a delete
costs any index keyed on slot position.

Settle before writing code:

- [ ] **Keyed on slot position, or on file offset?** Delete shifts slots; compaction
      changes offsets. Slot-keyed (indirecting through the existing offsets array)
      makes compaction nearly free and delete the expensive one. Offset-keyed
      inverts that. This is the reason indexes come after both.
- [ ] **Documents missing the indexed field.** If they are simply absent from the
      index, then `$exists:false` and `$ne` cannot be answered from it and the
      planner needs a scan fallback for negation. MongoDB has the same wart. Decide
      it up front, or meet it as a wrong-results bug.

Then build. Settle these while building rather than before: multikey entries for
array fields (`tags` needs this immediately — one index entry per element is what
makes `{"tags": "code"}` indexable), cross-type ordering that matches the documented
sort order or range scans silently lie, explicit `CreateIndex` versus automatic, and
where an index definition is recorded.

---

## 6. Scale tests — TypeScript and Python

See [`testing.md`](testing.md) §5 for where these live.

**Measure in the language the database is used from.** The number that matters is the
one a caller experiences end to end, through the FFI boundary and the JSON
marshalling, not the engine in isolation — a Go figure of 0.1 ms means nothing to a
caller waiting 5 s. So the consuming language sets the budget, and every language
that is actually shipped gets its own suite. This is where conformance's
"test once, run everywhere" reasoning stops applying: semantics are shared,
performance is not.

- [ ] TypeScript suite — defines the budget, end to end.
- [ ] Python suite — same.
- [ ] Report the **split**: total elapsed versus time inside the library. "1 M
      documents takes 5 s" is not actionable; "4.6 s of it is `JSON.parse`" is.
- [ ] Go benchmarks as the diagnostic layer — `testing.B`, `-benchmem`, pprof. These
      are how you find *where* a blown budget went; optimisation still happens in Go.
      They are never the acceptance criterion.

Expect marshalling to dominate. `capi.go`'s `nsq_find` already carries a note that it
materialises the whole result as one C string on top of the Go slice, and that the
real fix is a batched `nsq_find_batch` (§11 item 5). These tests are the evidence
that would justify building it.

---

## Unscheduled

No position in the order yet.

- [ ] **Multi-process exclusion** — `flock` on the database file (§11 item 7).
      Nothing stops two processes opening one file today, which stops being
      theoretical the moment this is installed into a real app (item 4).
- [ ] **Fuzz the replay path** — Go has native fuzzing and `Open` against truncated
      or corrupt files is a natural target. Cheap, and this is a storage engine.
- [ ] **`Update(filter, {$set: ...})`** — the operator-style sibling of `Replace`.
      The name is deliberately still free.
- [ ] **`Replace` returns 0 on a failed sync.** If `syncIfNeeded` fails after the
      index has already been updated, `Replace` reports `(0, err)` even though the
      document was replaced in memory. `DeleteMany` deliberately does not copy this,
      returning what actually landed; `Replace` should be brought into line.
- [ ] **Partial parsing** — §11 item 4, the single biggest scan-speed win available.
      Worth much more once projections (item 3) land: `RequiredPaths()` can then be
      filter-fields ∪ projected-fields, so even a matching document never needs a
      full decode.
- [ ] Quick-start examples in `README.md`, both binding module docstrings and
      `examples/basic/*` still show only insert and find. Adding `Replace` and
      `Delete` means doing all three languages, since they are kept parallel.
