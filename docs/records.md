# Records: insert, replace, delete

Every write is an append. Nothing on disk is ever modified in place, so the file
keeps every version of every document; the in-memory index is what picks the
current one ([`collection.go`](../collection.go), [`index.go`](../index.go),
[`store.go`](../store.go)).

> **A record is dead if, and only if, nothing in the in-memory index points at it.**

There is no free list, no allocation bitmap and no per-record live flag, because
setting one would mean writing in place — which breaks the record's CRC, breaks
torn-tail recovery, and breaks lock-free readers, all at once.

---

## The three writes

| | record appended | payload | index afterwards | `Len()` |
|---|---|---|---|---|
| **insert** | `op=1` | the whole document | **+1 slot** pointing at it | +1 |
| **replace** | `op=3` | the whole new document | slot `i` **re-pointed** at it | unchanged |
| **delete** | `op=2` | just the `_id` | **−1 slot**; nothing points at the tombstone | −1 |

Two vocabulary words, because the subtleties live in the gap between them:

| term | is | lives |
|---|---|---|
| **slot** / **position** `i` | where a document sits in `c.offsets` / `c.lengths` | memory |
| **offset** | where its bytes start in the file | disk |

**A replace keeps a document's position and changes its offset.** Most of this
document follows from that sentence.

---

## Insert

```go
id, err := users.Insert(map[string]any{"name": "Ada"})
```

What happens, in order:

1. encode the document to JSON,
2. `appendRecord(opInsert, …)` writes it at `db.size`, the end of the file,
3. `fsync`,
4. append `(offset, length)` to the collection's index arrays.

**Step 4 happens only if step 2 and 3 succeeded** — a reader must never see an
index entry pointing at a record that was not written.

`InsertMany` writes every record into one buffer, issues one `Write` and syncs
once. It is **not atomic**: a crash can leave a prefix of the batch, and the
return value says how many landed.

`_id` is generated if absent — 8 bytes of big-endian Unix nanoseconds plus 8
random bytes, hex-encoded, so ids sort by creation order and collisions are not
a thing that happens. A caller-supplied `_id` must be a non-empty string and
unique within the collection.

## Replace

```go
n, err := users.Replace(map[string]any{"_id": "ord-1"}, newDoc)   // n is 0 or 1
```

**Payload size does not matter.** Larger, smaller, identical — it is one append
either way. That is the biggest thing the append-only layout buys: no size
class, no best-fit search, no overflow page, no forwarding pointer.

**The payload is the complete new document, not a diff.** A diff would make
reading a document cost "base record plus every diff since", and make replay
order-dependent — trading a solved problem (bytes on disk, which compaction
reclaims) for an unsolved one.

**`_id` is immutable.** The replacement always keeps the `_id` of the document
the filter matched. What the replacement itself carries only decides whether the
write happens at all:

| `_id` in the replacement | result |
|---|---|
| absent | matched document's `_id` is kept |
| same as the matched document's | same, plus a free assertion that you hit the right one |
| **different** | `ErrImmutableID` — nothing is written |

The mismatch is worth an error because an `_id` in the replacement is not a
second way to pick the document — the filter always picks. Leave it out and a
filter that selects the wrong document overwrites it, silently and irreversibly.
So the error names both sides and where each came from:

```
the filter matched document "ord-1", but the replacement carries _id "ord-2";
one of them is wrong
```

**The document keeps its slot**, so insertion order is preserved: a replaced
document stays where it was in an unsorted result instead of jumping to the end.

## Delete

```go
n, err := users.Delete(filter)       // at most 1
n, err := users.DeleteMany(filter)   // every match
```

The write is one append of `{"_id":"ord-1"}` with `op=2`. Nothing is erased; the
original record stays byte-for-byte where it was.

**The tombstone is a message to future replays, not to queries.** Memory is
derived state, rebuilt from the file on every `Open` — drop the slot in memory
only, restart, and replay finds the original insert record and the document
comes back. No running query ever reads an `op=2` record.

**The slot is removed, not marked dead.** Deleting the document at position 1
shifts every later position down by one, so the index holds exactly one slot per
live document — never a mix of live and dead ones. The document count is
therefore the index's own length, which is why counting with no filter is a
lookup rather than a scan and never touches the file.

**The shift costs nothing extra.** Dropping an entry from the middle of a list
normally means moving everything after it down — but a delete rebuilds the index
from scratch regardless, because a reader may still be walking the old one
([below](#what-mutation-costs-the-fast-paths)). Every surviving slot is being
copied into a new index either way; the deleted ones are simply not copied.

**A filter argument is required**, in every language. `find` and `count` let you
leave it out and read that as "everything", which is harmless. Here the same
default would empty the collection. Delete is the one operation in this API with
no undo, so emptying a collection must not be the easiest thing to type — the
same reason `Delete` and `DeleteMany` are separate calls, and the reason Mongo
splits `deleteOne` from `deleteMany`.

**Deleting makes the file bigger.** The original bytes stay where they are and a
tombstone is appended on top, so a deleted 300-byte document leaves ~340 dead
bytes behind — the abandoned record plus the tombstone naming its `_id`.
[`Compact`](compaction.md) is what turns that back into zero; nothing else does.

---

## Worked example: three inserts and one delete

Real offsets, as `nsq dump` prints them. The index holds the *payload* offset,
which is the record offset + 12.

```
file                                            index — users
off:  32      67      103     141               position:    0     1     2
    +-------+-------+-------+-------+           offset:     79   115   153
    | def   | ins   | ins   | ins   |           length:     24    26    25
    | users | _id 1 | _id 2 | _id 3 |           (_id:      "1"   "2"   "3")
    +-------+-------+-------+-------+
```

`Delete({"_id": "2"})` appends an 11-byte payload naming only the `_id`:

```
file                                                    index — users
off:  32      67      103     141     178               position:    0     1
    +-------+-------+-------+-------+-------+           offset:     79   153
    | def   | ins   | ins   | ins   | del   |           length:     24    25
    | users | _id 1 | _id 2 | _id 3 | _id 2 |           (_id:      "1"   "3")
    +-------+-------+-------+-------+-------+
                      ^                ^
                      |                nothing points at this record — ever
                      still on disk, now unreachable
```

`Len()` is 2, a query returns documents 1 and 3, the file grew from 178 to 201
bytes, and `DeadBytes()` is 61 — the abandoned insert (12 + 26) plus the
tombstone (12 + 11), which is garbage the moment it is written.

---

## Replay rebuilds all of it

`Open` walks the file once and reconstructs the index from the records alone.
Insert records are never parsed — replay records where the payload is and how
long it is. A replace or a delete has to know *which slot it supersedes*, and
the only stable answer is the `_id`:

```
op=1  ->  append slot                              (no parse)
op=3  ->  resolve _id -> i, re-point slot i
op=2  ->  resolve _id -> i, set lengths[i] = 0     <- mark, do not remove

once, at the end:
    drop every zero-length slot, rebuild that collection's idTable
```

**Why mark instead of remove.** The idTable that resolves `_id`s is keyed by slot
number, so removing a slot mid-walk would shift every later slot and change what
an already-recorded position means. Marking keeps positions valid for the rest of
the walk. A zero length works as the marker because no real slot can
have one: the shortest payload a record can hold is `{"_id":"x"}`, and the live
delete path removes a slot rather than zeroing it.

**The idTable is built lazily**, on a collection's first `op=2`/`op=3` record, by
reading back the documents indexed so far. Building it unconditionally would add
+24 bytes/doc to the 12-byte headline — a 3× increase for every database,
including ones that never mutate anything. Three consequences:

- A file that was never mutated pays **nothing**; the parse-free property holds.
- Once a collection's table exists, its *later inserts must be parsed for `_id`
  too*, or a replace naming one of them cannot resolve.
- `WithFastOpen` cannot skip a replace or delete payload — that payload is the
  only place the record's `_id` appears.

**A record naming an `_id` the collection does not hold is `ErrCorrupt`.** The
file disagrees with itself; `Open` refuses rather than guessing.

**Delete then re-insert the same `_id` works**, because the tombstone takes the
`_id` out of the idTable as well as marking the slot:

```
op=1  {"_id":"ord-1", v:1}    ->  slot 0, idTable[ord-1] = 0
op=2  {"_id":"ord-1"}         ->  slot 0 dead, ord-1 out of the idTable
op=1  {"_id":"ord-1", v:2}    ->  slot 1                    <- no duplicate error
```

**Later record wins, always** — guaranteed by there being exactly one append
point for the whole database, so every record has an unambiguous position in one
global order.

---

## Crash safety

A write has three crash windows, and all three land somewhere correct:

| crash point | on disk | after `Open` | correct? |
|---|---|---|---|
| before any bytes | nothing | unchanged | yes — the call never returned |
| **mid-write** | torn record at the tail | truncated away, unchanged | yes — the call never returned |
| after write + `fsync` | complete record | applied | yes — the call returned success |

There is no fourth case, and no state in which the database is half-mutated.
That is inherited wholesale from the append path rather than being a separate
safety story per operation.

The contrast is what makes it worth stating: an *in-place* delete — zeroing the
record, or flipping a live bit — interrupted halfway leaves a record whose bytes
no longer match its CRC **in the middle of the file**. The failure mode is not
"lost one document", it is "the database will not open at all". Appending keeps
damage confined to the tail.

Two limits, stated plainly:

- **Single-record atomicity only.** `DeleteMany` matching 100 documents writes
  100 tombstones; a crash can leave 60 gone and 40 alive. All-or-nothing needs
  the reserved `op=5`/`op=6` pair.
- **Atomic is not durable.** Under `SyncNever` a call that returned success can
  still vanish in a crash. That is the mode's documented trade.

---

## What mutation costs the fast paths

Three things in the engine assumed a slot's contents never change. Each one is
the reason for a piece of machinery that would otherwise look arbitrary.

**1. Index entries are immutable once published.** A scan copies two slice
headers under the read lock and then runs with **no lock held**, sharing the
writer's backing array. `offsets` and `lengths` are two separate arrays, so
there is no way to update both as one operation — a reader landing between the
two stores gets a new offset with an old length and reads truncated JSON, or
straddles into the next record. Silent corruption, and a data race by the Go
memory model besides.

So a mutation builds **new** arrays and swaps them in under the write lock.

Appending is exempt, and Go's three-index slice is why. `snapshot` takes
`c.offsets[0:n:n]` — low, high, *capacity* — which hands the reader a view whose
length **and** capacity are both `n`. The reader can therefore only ever see
positions `0…n-1`, while the writer's next `append` lands at index `n`, outside
that view. Reallocation is equally invisible: the reader simply keeps the old
array alive.

> The copy is O(n) per **call**, not per document — 12 MB at a million
> documents, ≈1 ms, the same order as the `fsync` the default durability mode
> already pays. Mutation is filter-based precisely so one call can touch many
> documents and pay it once.

**2. `scanSequential` is only correct while nothing is superseded.** The
sequential path ignores the index and re-derives membership from the file's
`coll` byte, which is valid only while "every insert record with this coll id"
*is* the collection. One replace or delete makes it false. `Collection.dirty` is
set on the first mutation and pins every later scan to the strided path, which
reads the index and so knows exactly which records are live. One bool, one
branch, fails safe, cleared by `Compact`.

**3. Offsets stop ascending.** The replacement lands at the end of the file, so
`[44, 522, 990]` becomes `[44, 88712, 990]`. `scanStrided`'s forward-only
strided read becomes genuine random I/O.

That third point reframes what [compaction](compaction.md) is for: not primarily
space reclamation, but restoring the invariants the fast paths depend on.

---

## Why `Replace`, and why no `ReplaceMany`

The payload is a whole document, and in Mongo's own vocabulary whole-document is
`replaceOne`, not `updateOne`. Calling it `Update` would mean renaming it when
`$set` arrives, or shipping an `Update` that rejects `$set` forever.

`ReplaceMany(filter, doc)` would take **one** document and apply it to many
matches, leaving them byte-identical apart from their `_id` — and it could not
honour a supplied `_id` for more than one of them, so it would have to silently
ignore the field its single-document sibling validates. MongoDB omits it for the
same reason. Changing a field across many documents is `Update(filter, {$set:
…})`, which the naming leaves free.

**`Many` is for operations whose plural form is genuinely one operation**, not
for symmetry. `DeleteMany` qualifies; `ReplaceMany` does not.

---

## See also

- [`file-format.md`](file-format.md) — the record framing these ops share
- [`compaction.md`](compaction.md) — collecting what mutation leaves behind
- [`api.md`](api.md) — the calls in all three languages
