# A NoSQL document-store API, for people who know SQLite

This is a tour of what a "common NoSQL API" actually looks like, written for someone
fluent in SQL. It uses **MongoDB** as the reference dialect, because that is what most
people mean when they say "the NoSQL API", and because its query filters are plain
JSON — which maps 1:1 onto Python dicts and JavaScript objects, and is therefore what
`nosqlite` will copy.

The last section shows the same ideas in CouchDB, Firestore, and DynamoDB, so you can
see which parts are universal and which are Mongo-specific.

---

## 1. Terminology map

| SQLite | Document store | Notes |
| --- | --- | --- |
| database (file) | **database** | Same idea: a namespace holding many stores. |
| table | **collection** | The thing you asked about — "document store" and "collection" are the same concept. Mongo/CouchDB say *collection*, Firestore says *collection*, DynamoDB says *table*. |
| row | **document** | A JSON object. Not a flat tuple — it can nest. |
| column | **field** | A key inside the document. Documents in one collection need not share fields. |
| `PRIMARY KEY` | `_id` | Every document has one. Auto-generated if you don't supply it. Unique within the collection. |
| `CREATE TABLE` | *(nothing)* | Collections are created implicitly on first insert. |
| schema / `NOT NULL` / types | *(nothing enforced)* | The store will accept any shape. |
| `SELECT ... WHERE` | `find(filter)` | The filter is a **data structure**, not a string you parse. |
| `CREATE INDEX` | `createIndex` | Same concept, same reason. |
| `JOIN` | *(not really)* | You either embed the related data in the document, or do a second query. |

The single biggest mental shift: **the query is a document, not a sentence.** In SQL you
build a string. Here you build a nested dict/object and hand it over. That means no
string escaping, no SQL injection, and easy programmatic composition — but also no
`EXPLAIN`-shaped language to lean on.

And yes — to answer the question in `prompt.md` directly: **one database holds many
collections**, exactly the way one SQLite file holds many tables.

---

## 2. Creating a store

There is no `CREATE TABLE`. Inserting into a collection that doesn't exist creates it:

```python
db["users"].insert_one({"name": "Ada"})   # collection "users" now exists
```

You *can* create one explicitly, which is only needed when you want non-default options
(a size cap, schema validation, a specific collation):

```python
db.create_collection("users")
db.list_collection_names()     # ['users']
```

**What the missing schema costs you.** Everything SQLite's `CREATE TABLE` was doing for
you silently stops happening:

- A typo creates a field rather than raising. `{"nmae": "Ada"}` inserts happily, and
  `find({"name": "Ada"})` simply won't return it.
- No type enforcement. `age` can be `36` in one document and `"36"` in the next, and
  `{"age": {"$gte": 30}}` will match the first and not the second (see §5 on ordering).
- No `NOT NULL`, no `CHECK`, no foreign keys.
- "Migrations" become a read-time concern: old documents keep their old shape forever,
  so your code has to tolerate every version it has ever written, or you run a bulk
  rewrite yourself.

In exchange you get to change the shape of your data without a migration step, and you
get to store genuinely irregular data (per-user settings blobs, API payloads, event
records) without 40 nullable columns.

---

## 3. Inserting data

```python
users = db["users"]

# One document. _id is generated for you and attached to the object you passed in.
result = users.insert_one({
    "name": "Ada Lovelace",
    "age": 36,
    "email": "ada@example.com",
    "address": {"city": "London", "country": "UK"},   # nested object: fine
    "tags": ["math", "computing"],                    # array: fine
})
print(result.inserted_id)      # ObjectId('6560...')

# Many at once.
users.insert_many([
    {"name": "Grace Hopper", "age": 45, "tags": ["navy", "computing"]},
    {"name": "Alan Turing",  "age": 41, "tags": ["math"]},
    {"name": "Katherine Johnson", "age": 52},          # no "tags" field at all — allowed
])
```

Three things worth noticing:

1. **`_id` is assigned by the driver/server** if you don't provide one. You may supply
   your own (any scalar — a string, an int, a UUID); inserting a duplicate `_id` is the
   one uniqueness error you get for free.
2. **Nested objects and arrays are first-class values**, not blobs. You can filter and
   sort on `address.city` or on elements of `tags` without unpacking anything. This is
   the feature that replaces most of what you'd use a JOIN table for.
3. **Documents in one collection need not agree.** `Katherine Johnson` has no `tags`
   field. That is not `NULL` — the field is *absent*, and the two are distinguishable
   (`$exists`).

---

## 4. Reading and filtering

`find()` takes a **filter document** and returns a cursor; `find_one()` returns a single
document or `None`.

```python
users.find_one({"name": "Ada Lovelace"})
list(users.find({"age": {"$gte": 40}}))
users.count_documents({"age": {"$gte": 40}})
```

An empty filter `{}` means "everything" — the equivalent of `SELECT * FROM users`.

### Operators

Each key in the filter is a field name; the value is either a literal (meaning equality)
or a `{operator: operand}` object. Multiple keys are **implicitly ANDed**.

| Filter | SQL equivalent |
| --- | --- |
| `{"age": 36}` | `age = 36` |
| `{"age": {"$eq": 36}}` | `age = 36` (explicit form) |
| `{"age": {"$ne": 36}}` | `age <> 36` |
| `{"age": {"$gt": 30}}` | `age > 30` |
| `{"age": {"$gte": 30}}` | `age >= 30` |
| `{"age": {"$lt": 30}}` | `age < 30` |
| `{"age": {"$lte": 30}}` | `age <= 30` |
| `{"age": {"$in": [36, 41]}}` | `age IN (36, 41)` |
| `{"age": {"$nin": [36, 41]}}` | `age NOT IN (36, 41)` |
| `{"age": {"$gte": 30, "$lt": 50}}` | `age >= 30 AND age < 50` |
| `{"name": "Ada", "age": 36}` | `name = 'Ada' AND age = 36` |
| `{"$or": [{"age": {"$lt": 30}}, {"age": {"$gt": 50}}]}` | `age < 30 OR age > 50` |
| `{"$and": [{...}, {...}]}` | explicit AND (only needed when two conditions share a key) |
| `{"age": {"$not": {"$gt": 30}}}` | `NOT (age > 30)` |
| `{"email": {"$exists": True}}` | roughly `email IS NOT NULL`, but see below |
| `{"name": {"$regex": "^Ada"}}` | `name LIKE 'Ada%'` |

`$exists` deserves a note: it asks whether the **key is present**, which is not the same
as whether the value is null. `{"email": None}` and a document with no `email` key are
different states, and only the second is `{"$exists": False}`. SQLite has no equivalent
distinction.

### Dotted paths

To reach into a nested object, use a dotted string as the field name:

```python
users.find({"address.city": "London"})
users.find({"address.country": {"$in": ["UK", "US"]}})
```

There is no SQL equivalent short of `json_extract(data, '$.address.city')`.

### Arrays

This is the part that surprises SQL people most. When a field holds an array, a scalar
filter matches if **any element** matches:

```python
users.find({"tags": "math"})          # matches docs whose tags array CONTAINS "math"
users.find({"tags": ["math"]})        # matches only where tags is EXACTLY ["math"]
users.find({"tags": {"$all": ["math", "computing"]}})   # contains both
users.find({"tags": {"$size": 2}})    # array has exactly 2 elements
users.find({"scores": {"$gt": 90}})   # any element > 90
```

The catch: with two conditions on the same array, each may be satisfied by a *different*
element. `{"scores": {"$gt": 90, "$lt": 95}}` matches `[80, 99]` — 99 satisfies `$gt`, 80
satisfies `$lt`. If you meant "one element satisfies both", you need `$elemMatch`:

```python
users.find({"scores": {"$elemMatch": {"$gt": 90, "$lt": 95}}})   # needs a single 91–94
```

### Projections

The second argument to `find()` selects which fields come back — the `SELECT a, b` part:

```python
users.find({"age": {"$gte": 30}}, {"name": 1, "age": 1})       # only these (plus _id)
users.find({}, {"email": 0})                                    # everything except email
```

---

## 5. Sorting and paginating

Sort, skip, and limit are chained onto the cursor and applied by the server:

```python
from pymongo import ASCENDING, DESCENDING

users.find({"age": {"$gte": 30}}) \
     .sort([("age", DESCENDING), ("name", ASCENDING)]) \
     .skip(20) \
     .limit(10)
```

`DESCENDING` is just `-1` and `ASCENDING` is `1`; in JS you write `.sort({ age: -1 })`
directly. Order of operations is always filter → sort → skip → limit, regardless of the
order you chain them.

**The ordering problem.** Because there is no schema, a single field can hold values of
different types across documents, and some documents may not have the field at all. So
"sort by age" must be defined even when ages are `36`, `"unknown"`, `null`, and missing.
Mongo solves this by defining a **total order across types**, roughly:

```
null  <  numbers  <  strings  <  objects  <  arrays  <  booleans  <  dates
```

and by treating a **missing field as null** for sorting purposes. The result is
deterministic but occasionally surprising: `{"age": {"$gte": 30}}` will not match
`{"age": "36"}`, because a string is never `>=` a number — comparison operators only
compare *within* a type.

This is a decision every document store has to make, and it is one `nosqlite` will have
to make too (see the design doc, §5).

---

## 6. Indexes

Same concept as SQLite, same reasons, and it matters more here:

```python
users.create_index([("age", DESCENDING)])
users.create_index([("address.city", ASCENDING), ("age", DESCENDING)])   # compound
users.create_index([("email", ASCENDING)], unique=True)
users.list_indexes()
```

`_id` is indexed automatically. Everything else is a **full collection scan** until you
add an index, and nothing warns you — a `find()` on an unindexed field over a million
documents just quietly takes a long time. The equivalent of `EXPLAIN QUERY PLAN` is
`.explain()`, and it is worth reaching for at the same moments you'd reach for it in
SQLite.

A `unique=True` index is also how you get the constraint that `UNIQUE` gave you in DDL —
uniqueness is an index property here, not a column property.

---

## 7. Everything else (not in scope for nosqlite v1)

Named here so you know the shape of the rest of the API and can see what v1 is leaving
out:

```python
users.update_one({"name": "Ada"}, {"$set": {"age": 37}})
users.update_many({"age": {"$lt": 18}}, {"$set": {"minor": True}})
users.update_one({"name": "Ada"}, {"$inc": {"logins": 1}, "$push": {"tags": "poetry"}})
users.replace_one({"name": "Ada"}, {...})       # whole-document swap
users.delete_one({"name": "Ada"})
users.delete_many({"age": {"$lt": 18}})

# Aggregation pipeline — the GROUP BY / window-function tier.
users.aggregate([
    {"$match": {"age": {"$gte": 30}}},
    {"$group": {"_id": "$address.country", "count": {"$sum": 1}, "avgAge": {"$avg": "$age"}}},
    {"$sort": {"count": -1}},
])
```

Note the two distinct vocabularies: `$gte`/`$in` are **query** operators (used in
filters), while `$set`/`$inc`/`$push` are **update** operators and `$match`/`$group` are
**pipeline stages**. They're not interchangeable.

---

## 8. The same task in three languages

Insert some users, then find everyone 30 or older, newest first, ten at a time.

### Python (`pymongo`)

```python
from pymongo import MongoClient, DESCENDING

client = MongoClient("mongodb://localhost:27017")
db = client["appdb"]
users = db["users"]

users.insert_many([
    {"name": "Ada Lovelace", "age": 36, "address": {"city": "London"}},
    {"name": "Grace Hopper", "age": 45, "address": {"city": "New York"}},
    {"name": "Alan Turing",  "age": 41, "address": {"city": "London"}},
])

for user in users.find({"age": {"$gte": 30}}).sort("age", DESCENDING).limit(10):
    print(user["name"], user["age"])

client.close()
```

### TypeScript (`mongodb` Node driver)

```ts
import { MongoClient } from "mongodb";

const client = new MongoClient("mongodb://localhost:27017");
await client.connect();

const users = client.db("appdb").collection("users");

await users.insertMany([
  { name: "Ada Lovelace", age: 36, address: { city: "London" } },
  { name: "Grace Hopper", age: 45, address: { city: "New York" } },
  { name: "Alan Turing",  age: 41, address: { city: "London" } },
]);

const docs = await users
  .find({ age: { $gte: 30 } })
  .sort({ age: -1 })
  .limit(10)
  .toArray();

for (const u of docs) console.log(u.name, u.age);

await client.close();
```

### SQLite, for comparison

```sql
CREATE TABLE users (
  id    INTEGER PRIMARY KEY,
  name  TEXT NOT NULL,
  age   INTEGER,
  city  TEXT            -- the nested address had to be flattened
);

INSERT INTO users (name, age, city) VALUES
  ('Ada Lovelace', 36, 'London'),
  ('Grace Hopper', 45, 'New York'),
  ('Alan Turing',  41, 'London');

SELECT * FROM users WHERE age >= 30 ORDER BY age DESC LIMIT 10;
```

The differences to take away: the schema had to be declared up front and the nested
`address` had to be flattened into a column, but in return the SQL version rejects a
missing `name` and a non-integer `age`, and the query is one readable line.

---

## 9. Other dialects, briefly

The vocabulary changes; the shape does not. Every one of these is *collection +
document + declarative filter + sort + limit*.

**CouchDB (Mango)** — closest to Mongo; the filter is called a `selector` and the whole
query is one JSON body posted to `/{db}/_find`:

```json
{
  "selector": { "age": { "$gte": 30 } },
  "sort": [{ "age": "desc" }],
  "limit": 10
}
```

**Firestore** — same model, but the filter is built from function calls rather than a
literal object, and each condition is a separate `where(...)`:

```ts
const q = query(
  collection(db, "users"),
  where("age", ">=", 30),
  orderBy("age", "desc"),
  limit(10),
);
const snap = await getDocs(q);
snap.forEach(d => console.log(d.id, d.data()));
```

**DynamoDB** — the outlier, and instructive because of *how* it differs. A `Query` can
only run against a partition key; anything else is a `Scan` with a filter applied after
reading, and filtering does not reduce what you're billed for. Sorting is limited to the
sort key's direction. Values carry explicit type tags (`{"N": "30"}`):

```json
{
  "TableName": "users",
  "KeyConditionExpression": "pk = :pk AND age >= :age",
  "ExpressionAttributeValues": { ":pk": {"S": "USER"}, ":age": {"N": "30"} },
  "ScanIndexForward": false,
  "Limit": 10
}
```

DynamoDB is the reminder that "NoSQL" is not one API — it's a family. The
Mongo/Couch/Firestore end of that family is the part worth copying for `nosqlite`,
because arbitrary filter + arbitrary sort is exactly the ergonomic you're used to from
SQLite.

---

## What nosqlite takes from this

- Collections created implicitly on first insert (§2).
- Documents are arbitrary JSON with an auto-assigned `_id` (§3).
- Filters are **JSON documents in the Mongo dialect** (§4) — the subset
  `$eq $ne $gt $gte $lt $lte $in $nin $exists $and $or $not`, with dotted paths and
  implicit AND. This choice is what lets the Go, Python, and TypeScript APIs be
  literally the same filter value, with no translation layer.
- Sort / skip / limit with an explicitly documented cross-type ordering (§5).
- Whole-document **replace** is supported (§4's filter picks the document; the new
  document overwrites it entirely). Indexes, operator-style updates (`$set`), deletes,
  projections, and aggregation are **out of scope for v1**, but the design leaves a
  named place for each.

See [`design.md`](design.md) for how that gets built.
