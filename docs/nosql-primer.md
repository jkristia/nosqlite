# A document store, for people who know SQLite

What "the NoSQL API" actually looks like. MongoDB is the reference dialect,
because that is what most people mean by it and because its filters are plain
JSON — which maps 1:1 onto Python dicts and JavaScript objects, and is what
nosqlite copies.

---

## Terminology

| SQLite | Document store | Notes |
| --- | --- | --- |
| database (file) | **database** | a namespace holding many stores |
| table | **collection** | Mongo/CouchDB/Firestore say *collection*; DynamoDB says *table* |
| row | **document** | a JSON object — not a flat tuple, it can nest |
| column | **field** | a key inside the document; documents need not share fields |
| `PRIMARY KEY` | `_id` | every document has one, generated if you don't supply it |
| `CREATE TABLE` | *(nothing)* | collections are created on first insert |
| schema, `NOT NULL`, types | *(nothing enforced)* | any shape is accepted |
| `SELECT … WHERE` | `find(filter)` | the filter is a **data structure**, not a string |
| `CREATE INDEX` | `createIndex` | same concept, same reasons |
| `JOIN` | *(not really)* | embed the related data, or do a second query |

**The biggest mental shift: the query is a document, not a sentence.** You build
a nested dict and hand it over. No string escaping, no injection, easy
programmatic composition — but also no `EXPLAIN`-shaped language to lean on.

One database holds many collections, exactly as one SQLite file holds many
tables.

---

## Insert

```python
users = db["users"]

users.insert_one({
    "name": "Ada Lovelace",
    "age": 36,
    "address": {"city": "London", "country": "UK"},   # nested object: fine
    "tags": ["math", "computing"],                    # array: fine
})

users.insert_many([
    {"name": "Grace Hopper", "age": 45, "tags": ["navy"]},
    {"name": "Katherine Johnson", "age": 52},          # no "tags" at all — allowed
])
```

- **`_id` is generated** if you don't supply one. A duplicate `_id` is the one
  uniqueness error you get for free.
- **Nested objects and arrays are first-class**, not blobs — you can filter and
  sort on `address.city` or on elements of `tags`. This is what replaces most of
  what you'd use a JOIN table for.
- **Documents need not agree.** Katherine has no `tags` field. That is not
  `NULL` — the field is *absent*, and the two are distinguishable.

**What the missing schema costs you.** Everything `CREATE TABLE` was doing
silently stops:

- A typo creates a field rather than raising. `{"nmae": "Ada"}` inserts happily.
- No type enforcement. `age` can be `36` here and `"36"` there.
- "Migrations" become a read-time concern: old documents keep their old shape
  forever, so your code tolerates every version it ever wrote — or you run a bulk
  rewrite yourself.

In exchange you change the shape of your data without a migration, and you can
store genuinely irregular data without 40 nullable columns.

---

## Filter

Each key is a field name; the value is a literal (equality) or a
`{operator: operand}` object. Multiple keys are implicitly ANDed. `{}` means
everything.

| Filter | SQL |
| --- | --- |
| `{"age": 36}` | `age = 36` |
| `{"age": {"$ne": 36}}` | `age <> 36` |
| `{"age": {"$gt": 30}}` | `age > 30` — also `$gte`, `$lt`, `$lte` |
| `{"age": {"$in": [36, 41]}}` | `age IN (36, 41)` — also `$nin` |
| `{"age": {"$gte": 30, "$lt": 50}}` | `age >= 30 AND age < 50` |
| `{"name": "Ada", "age": 36}` | `name = 'Ada' AND age = 36` |
| `{"$or": [{…}, {…}]}` | `OR` |
| `{"age": {"$not": {"$gt": 30}}}` | `NOT (age > 30)` |
| `{"email": {"$exists": true}}` | roughly `email IS NOT NULL` — see below |
| `{"name": {"$regex": "^Ada"}}` | `name LIKE 'Ada%'` |
| `{"address.city": "London"}` | `json_extract(data, '$.address.city') = 'London'` |

Three things that surprise SQL people:

**`$exists` asks whether the key is present**, which is not whether the value is
null. `{"email": None}` and a document with no `email` key are different states,
and only the second is `{"$exists": false}`. SQLite has no equivalent
distinction.

**An array matches if any element matches.**

```python
users.find({"tags": "math"})     # tags CONTAINS "math"
users.find({"tags": ["math"]})   # tags is EXACTLY ["math"]
users.find({"scores": {"$gt": 90}})   # any element > 90
```

The catch: two conditions on one array may each be satisfied by a *different*
element. `{"scores": {"$gt": 90, "$lt": 95}}` matches `[80, 99]`. If you meant
"one element satisfies both", that is `$elemMatch`.

**Comparison happens within a type.** `{"age": {"$gte": 30}}` does not match
`{"age": "36"}` — a string is never `>=` a number.

---

## Sort, skip, limit

```python
users.find({"age": {"$gte": 30}}).sort([("age", -1), ("name", 1)]).skip(20).limit(10)
```

`-1` is descending, `1` ascending. Order of operations is always **filter → sort
→ skip → limit**, no matter how you chain them.

**The ordering problem.** Without a schema, one field can hold different types
across documents, and some documents may not have it at all — so "sort by age"
has to be defined when ages are `36`, `"unknown"`, `null` and missing. Mongo
answers with a **total order across types** and treats a missing field as null.
Deterministic, and occasionally surprising. Every document store has to make this
decision; nosqlite's is in [`filters.md`](filters.md).

## Projection

Which fields come back — the `SELECT a, b` part:

```python
users.find({"age": {"$gte": 30}}, {"name": 1, "age": 1})   # only these (plus _id)
users.find({}, {"email": 0})                                # everything except email
```

## Indexes

Same concept and reasons as SQLite, and it matters more here: `_id` is indexed
automatically and **everything else is a full collection scan** until you add an
index — with nothing warning you. A `find()` on an unindexed field over a million
documents just quietly takes a long time.

```python
users.create_index([("age", -1)])
users.create_index([("email", 1)], unique=True)
```

`unique=True` is also how you get the constraint `UNIQUE` gave you in DDL:
uniqueness is an index property here, not a column property.

## Update, delete, aggregate

```python
users.update_one({"name": "Ada"}, {"$set": {"age": 37}})
users.update_one({"name": "Ada"}, {"$inc": {"logins": 1}, "$push": {"tags": "poetry"}})
users.replace_one({"name": "Ada"}, {...})       # whole-document swap
users.delete_many({"age": {"$lt": 18}})

users.aggregate([                                # the GROUP BY tier
    {"$match": {"age": {"$gte": 30}}},
    {"$group": {"_id": "$address.country", "count": {"$sum": 1}}},
])
```

Note the three vocabularies, which are not interchangeable: `$gte`/`$in` are
**query** operators, `$set`/`$inc`/`$push` are **update** operators, and
`$match`/`$group` are **pipeline stages**.

---

## Other dialects, briefly

The vocabulary changes; the shape does not. All of these are *collection +
document + declarative filter + sort + limit*.

- **CouchDB (Mango)** — closest to Mongo. The filter is a `selector` and the
  whole query is one JSON body posted to `/{db}/_find`.
- **Firestore** — same model, but built from function calls:
  `query(collection(db, "users"), where("age", ">=", 30), orderBy("age", "desc"))`.
- **DynamoDB** — the outlier, and instructive for *how* it differs. A `Query` can
  only run against a partition key; anything else is a `Scan` whose filter is
  applied after reading, and filtering does not reduce what you are billed for.

DynamoDB is the reminder that "NoSQL" is not one API but a family. The
Mongo/Couch/Firestore end is the part worth copying, because arbitrary filter
plus arbitrary sort is the ergonomic you already have from SQLite.

---

## What nosqlite takes from this

- Collections created implicitly on first insert; documents are arbitrary JSON
  with an auto-assigned `_id`.
- **Filters are JSON documents in the Mongo dialect** — the subset
  `$eq $ne $gt $gte $lt $lte $in $nin $exists $and $or $not`, with dotted paths
  and implicit AND. This is what lets the Go, Python and TypeScript APIs pass
  literally the same filter value, with no translation layer.
- Sort / skip / limit with an explicitly documented cross-type ordering, and
  Mongo's projection rules.
- Whole-document `Replace` and filter-based `Delete`/`DeleteMany`.
- **Not in v1:** indexes, operator updates (`$set`), aggregation — each with a
  named place in [`design.md`](design.md) for it to land.
