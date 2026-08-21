# Compaction

**Not built yet.** This is the design for it, and the accounting that already
exists to tell you when you need it.

Every replace and delete leaves the superseded bytes in the file
([`records.md`](records.md)), so a database that is mutated grows without bound.
Compaction rewrites the file keeping only live records.

---

## Where it stands

| | |
|---|---|
| `DB.DeadBytes()` — dead bytes in the open database | **done** |
| `ScanLive(path)` — the same accounting on a closed file | **done** |
| `nsq stat` reports it and suggests compacting | **done** |
| `Compact()` | not written |
| `nsq compact` CLI verb | not written |
| `DropCollection` — falls out of the same machinery | not written |

No format change is involved. But compaction rewrites every record, so **every
offset changes** — anything holding one (snapshots, and later any secondary
index) must be rebuilt or remapped across it.

---

## What it restores

Space is the obvious half and the smaller one. Compaction is what puts the
engine's fast paths back:

| restored | consequence |
|---|---|
| no dead records | file size back to live size |
| offsets ascend again | `scanStrided` is forward-only again |
| file order == insertion order | `scanSequential` becomes usable again (`dirty` clears) |
| records regrouped per collection | the interleaving penalty goes away |

A compacted file also contains no `op=2` or `op=3` records at all — every live
document is rewritten as a plain insert — so it reopens on the parse-free replay
path.

## How

Write a new file, then rename. Never in place: in-place rewriting would violate
every rule the design rests on.

```
1. take the write lock
2. create <path>.compact
3. write a fresh 32-byte file header
4. for each collection:
     write its define-collection record
     walk its live index in order, copying each live payload as a fresh op=1
     build the new offsets/lengths as you go
5. fsync the new file
6. rename <path>.compact -> <path>          (atomic on POSIX)
7. swap in the new *os.File and the new index arrays; db.dead = 0
8. release the lock
```

Step 4 is why this is cheap to write: it is `WalkFile` plus `appendRecord`, both
of which already exist.

## In-flight readers hold offsets into the old file

A scan runs with no lock held, for possibly seconds, against offsets that step 7
invalidates. So [`snapshot`](../index.go) captures the `*os.File` alongside the
offsets, and both scan paths read through `snap.file` — without that, a scan
that started before the swap would pair old offsets with the new file and land
on arbitrary record boundaries. That part is already in place.

On Unix that is sufficient: `rename(2)` over a path whose old inode is still open
keeps that inode alive until the last descriptor closes. Go's GC does not close
files, so the old `*os.File` has to be closed explicitly once no snapshot
references it — the one place refcounting is genuinely needed.

**On Windows this does not work.** Renaming over a file that is still open fails,
and the `.dll` is a first-class target. Options: keep the new file under a
generation-suffixed name and never rename, or block compaction until in-flight
scans drain. Unresolved, and the reason compaction is not a one-afternoon task.

## When

**Manual `Compact()` only** — no background goroutine, no policy, no tuning
knobs. One exported method the caller schedules.

What makes that actionable is one counter on `DB`, incremented by
`recordHeaderSize + lengths[i]` every time a slot is superseded:

```
$ nsq stat demo.nsq
dead        222 bytes (38%)  — consider `nsq compact demo.nsq`
```

`ScanLive(path)` reconstructs the same number from the file alone, so `nsq stat`
can report it on a database no process has open. It costs an extra pass parsing
every payload's `_id`, so it runs only when a cheap first pass finds an `op=2` or
`op=3` record.

Automatic compaction is a threshold on `dead/size` once there are real numbers to
set it from. The counter has to exist either way.

---

## Open questions

- **Does `Compact()` need to be interruptible?** Compacting a 300 GB file is
  minutes of held write lock. An incremental compactor is a much larger design;
  the honest answer may be "document that it blocks writes for the duration".
- **The Windows story** — see above. It gates shipping compaction on a platform
  where the `.dll` is meant to work.

---

## See also

- [`records.md`](records.md) — what creates the garbage, and the invariants this restores
- [`file-format.md`](file-format.md) — the framing a compacted file is rewritten into
- [`todo.md`](todo.md) — where this sits in the order of work
