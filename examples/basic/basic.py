#!/usr/bin/env python3
"""A tour of the Python API.

Run it from the repository root, after building the shared library:

    make build
    make example-py       # or: PYTHONPATH=python python3 examples/basic/basic.py

It writes ./db/example-py.nsq (and ./db/example-py.nsq.trace) and leaves both
behind when it exits, so you can inspect them afterwards:

    cat db/example-py.nsq.trace
    ./bin/nsq stat db/example-py.nsq
"""

import os
import sys

# Make `import nosqlite` work straight from a source checkout, without
# installing anything. This file lives in examples/basic/, so the package
# directory is two levels up.
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "python"))

from nosqlite import Database, NoSQLiteError  # noqa: E402

DIR = "./db"
PATH = DIR + "/example-py.nsq"


def main() -> None:
    # exist_ok=True makes this a no-op when the directory is already there,
    # instead of raising FileExistsError.
    os.makedirs(DIR, exist_ok=True)

    # Start from scratch each run, so the numbers printed below are predictable
    # -- otherwise every run would append another five people to the same
    # collection. Nothing is deleted at the end: both files stay on disk.
    for leftover in (PATH, PATH + ".trace"):
        if os.path.exists(leftover):
            os.remove(leftover)

    # The context manager closes the database on the way out, including if an
    # exception is raised.
    with Database(PATH, trace="all") as db:
        users = db["users"]

        # --- inserting ----------------------------------------------------

        ada_id = users.insert(
            {
                "name": "Ada",
                "age": 36,
                "tags": ["math", "code"],
                "address": {"city": "London"},
            }
        )
        print("inserted Ada with _id:", ada_id)

        users.insert_many(
            [
                {"name": "Grace", "age": 45, "address": {"city": "New York"}},
                {"name": "Alan", "age": 41, "tags": ["math"]},
                {"name": "Edsger", "age": 41, "address": {"city": "Austin"}},
                {"name": "Barbara", "age": 63, "tags": ["biology", "code"]},
            ]
        )
        print("collection now holds:", len(users), "documents")

        # --- querying -----------------------------------------------------

        print("\n-- everyone 40 or older, oldest first --")
        for u in users.find({"age": {"$gte": 40}}, sort=[("age", -1)], limit=10):
            # Numbers come back as Python ints here: json.loads undoes the
            # float64 round trip that happens inside Go.
            print(f"  {u['name']:<8} {u['age']}")

        print("\n-- anyone tagged 'math' (arrays match element-wise) --")
        for u in users.find({"tags": "math"}):
            print(" ", u["name"])

        print("\n-- dotted paths reach into nested objects --")
        # find_one returns None when nothing matches, so it has to be checked
        # before subscripting -- a type checker insists, and it is right to.
        austin = users.find_one({"address.city": "Austin"})
        print(" ", austin["name"] if austin else "nobody", "lives in Austin")

        print("\n-- $or, and counting --")
        n = users.count({"$or": [{"age": {"$lt": 40}}, {"age": {"$gt": 60}}]})
        print(f"  {n} people are under 40 or over 60")

        print("\n-- find_one on no match returns None --")
        print(" ", users.find_one({"name": "Nobody"}))

        # --- streaming ------------------------------------------------------

        # iter_find pages under the hood, so Python-side memory stays flat no
        # matter how many documents match.
        print("\n-- streaming with iter_find --")
        total = sum(u["age"] for u in users.iter_find(batch=2))
        print(f"  average age: {total / len(users):.1f}")

        # --- errors ---------------------------------------------------------

        print("\n-- errors come back as exceptions --")
        try:
            users.find({"age": {"$gtee": 30}})  # typo in the operator
        except NoSQLiteError as exc:
            print(" ", exc)

        print("\ncollections in this database:", db.collections())

    print("\nDatabase file:", PATH)
    print("Trace of everything above is in", PATH + ".trace")
    print("Both files are left in place — try: cat " + PATH + ".trace")
    print("Try: NOSQLITE_TRACE=verbose python3 examples/basic/basic.py")


if __name__ == "__main__":
    main()
