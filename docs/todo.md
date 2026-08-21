# Todo

The working list: what to build next, in order, with the decisions that have to be
made before each one can start.

**Keep it live.** Tick boxes as they land, and delete a section outright once its
item is finished — this file says what is left to do, not what was done. Notes that
outlive the task belong in the design docs instead.

This is the *task* view. [`design.md`](design.md) §8 is the *architectural* view —
the same work seen as extension points. §8 is ordered by design dependency; this
file is ordered by what to actually do next. Where they disagree about ordering,
this file wins; where they disagree about mechanism, §8 does.

---

## 0. Finish the docs pass

The docs were restructured on 2026-08-20 — one doc per area, README as an index.
`README.md`, `api.md`, `filters.md`, `records.md` and `design.md` have since had a
second pass against the style below. The rest have not been re-read with it.

**The style, so it does not have to be re-derived:**

- Lead with the *hows*: a short list of how to do the thing, a few lines of code,
  no more. Lookup tables beat prose. Reasoning comes last, one sentence per rule.
- State a rule forward — condition, then consequence. Drop the "but not in case X"
  clause when the condition already excludes X.
- No cryptic half-sentences. "Values are typed `unknown`, so narrow with
  `Number(u.age)`" tells a reader nothing; three annotated lines of code do.
- Short paragraphs. If it takes a paragraph to find one fact, it is too long.

**Still to re-read:**

- [ ] `file-format.md`
- [ ] `trace-and-cli.md`
- [ ] `bindings.md` — only the heading was touched
- [ ] `compaction.md`
- [ ] `testing.md`
- [ ] `getting-started.md`
- [ ] `nosql-primer.md`
- [ ] `compression.md` — the 156-line harness appendix: keep in the doc, or move
      to a runnable file?
- [ ] `go-primer/` — untouched by the restructure

Also worth doing while in there: the binding docstrings still describe the default
`find` limit as raising "if that limit is actually hit"
(`python/nosqlite/__init__.py`, `typescript/nosqlite/index.ts`). The docs now say
what actually happens — 1000 or more matches — and the source should agree.

---

## 1. Compaction

Not optional now that delete has landed: `nsq stat` already prints "consider
`nsq compact <path>`" for a verb that does not exist yet, and a delete makes the
file bigger until it does. Designed in [`compaction.md`](compaction.md).

- [ ] `Compact()` rewrites the file keeping only live versions, regrouped by
      collection so scan locality is restored.
- [ ] `nsq compact` CLI verb.
- [ ] `DropCollection`, which falls out of the same machinery.
- [ ] Decide the Windows story: rename-over-open fails there.

No format change. Compaction rewrites every record, so **every offset changes**.
Anything holding an offset — snapshots, and later any index — must be rebuilt or
remapped across it.

---

## 2. npm packaging (local install)

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

## 3. Secondary indexes

A planner walking the Matcher tree to turn `cmpNode` / `inNode` on an indexed
field into a lookup, with the rest as a residual filter.

**Already decided by the design:** indexes rebuild from the log on open — nothing new
on disk, so no new op code and no `format` question. The existing lazy `idTable` is
the precedent to generalise: it is already a secondary index on `_id`, built on first
need, and `removeIndex`'s remap is the precedent for what a delete costs any index
keyed on slot position.

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

## 4. Scale tests — TypeScript and Python

See [`testing.md`](testing.md) for where these live.

**Measure in the language the database is used from.** The consuming language sets
the budget, and every language that is actually shipped gets its own suite. This is
where conformance's "test once, run everywhere" reasoning stops applying: semantics
are shared, performance is not.

- [ ] TypeScript suite — defines the budget, end to end.
- [ ] Python suite — same.
- [ ] Report the **split**: total elapsed versus time inside the library. "1 M
      documents takes 5 s" is not actionable; "4.6 s of it is `JSON.parse`" is.
- [ ] Go benchmarks as the diagnostic layer — `testing.B`, `-benchmem`, pprof. These
      are how you find *where* a blown budget went; optimisation still happens in Go.
      They are never the acceptance criterion.

Expect marshalling to dominate. `capi.go`'s `nsq_find` already carries a note that it
materialises the whole result as one C string on top of the Go slice, and that the
real fix is a batched `nsq_find_batch`. These tests are the evidence that would
justify building it.

---

## Unscheduled

No position in the order yet.

- [ ] **Multi-process exclusion** — `flock` on the database file. Nothing stops two
      processes opening one file today, which stops being theoretical the moment
      this is installed into a real app (item 2).
- [ ] **Fuzz the replay path** — Go has native fuzzing and `Open` against truncated
      or corrupt files is a natural target. Cheap, and this is a storage engine.
- [ ] **`Update(filter, {$set: ...})`** — the operator-style sibling of `Replace`.
      The name is deliberately still free.
- [ ] **`Replace` returns 0 on a failed sync.** If `syncIfNeeded` fails after the
      index has already been updated, `Replace` reports `(0, err)` even though the
      document was replaced in memory. `DeleteMany` deliberately does not copy this,
      returning what actually landed; `Replace` should be brought into line.
- [ ] **Partial parsing** — the single biggest scan-speed win available. Worth more
      now that projections have landed: `RequiredPaths()` can be filter-fields ∪
      projected-fields, so even a matching document never needs a full decode.
- [ ] **Examples show only insert and find.** Both binding module docstrings and
      `examples/basic/*` predate `Replace`, `Delete` and projections. Adding them
      means doing all three languages, since the examples are kept parallel.
