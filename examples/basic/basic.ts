/**
 * A tour of the TypeScript API.
 *
 * Run it from the repository root, after building the shared library:
 *
 *     make build
 *     make example-ts       # or: node examples/basic/basic.ts
 *
 * There is no compile step: Node >= 22.18 strips the type annotations and runs
 * this file as-is. The only dependency is `koffi` (the FFI), installed by
 * `make example-ts` into typescript/node_modules.
 *
 * The two-line package.json next to this file exists only to say
 * `"type": "module"`. Node decides whether a file is an ES module (`import`)
 * or a CommonJS one (`require`) from the nearest package.json, and the repo
 * root has none — it is a Go project. Without it, tooling reads this file as
 * CommonJS and objects to the `import` lines below. Go and Python ignore it.
 *
 * It writes ./db/example-ts.nsq (and ./db/example-ts.nsq.trace) and leaves both
 * behind when it exits, so you can inspect them afterwards:
 *
 *     cat db/example-ts.nsq.trace
 *     ./bin/nsq stat db/example-ts.nsq
 */

import { mkdirSync, rmSync } from "node:fs";

// Imported by relative path so the example runs straight out of a checkout
// with nothing installed. The `.ts` extension is required: Node resolves the
// file literally, it does not guess extensions the way a bundler would.
import { Database, NoSQLiteError, type Document } from "../../typescript/nosqlite/index.ts";

const DIR = "./db";
const PATH = `${DIR}/example-ts.nsq`;

function main(): void {
  // recursive: true creates missing parents and — unlike the plain call —
  // succeeds when the directory is already there, so it is safe every run.
  mkdirSync(DIR, { recursive: true });

  // Start from scratch each run, so the numbers printed below are predictable
  // — otherwise every run would append another five people to the same
  // collection. Nothing is deleted at the end: both files stay on disk.
  for (const leftover of [PATH, `${PATH}.trace`]) {
    rmSync(leftover, { force: true }); // force: no error if it is not there
  }

  const db = new Database(PATH, { trace: "all" });
  // try/finally is this language's `defer db.Close()`: the database is closed
  // however we leave the block, exception or not.
  try {
    const users = db.collection("users");

    // --- inserting --------------------------------------------------------

    const adaId = users.insert({
      name: "Ada",
      age: 36,
      tags: ["math", "code"],
      address: { city: "London" },
    });
    console.log("inserted Ada with _id:", adaId);

    users.insertMany([
      { name: "Grace", age: 45, address: { city: "New York" } },
      { name: "Alan", age: 41, tags: ["math"] },
      { name: "Edsger", age: 41, address: { city: "Austin" } },
      { name: "Barbara", age: 63, tags: ["biology", "code"] },
    ]);
    console.log("collection now holds:", users.count(), "documents");

    // --- querying ---------------------------------------------------------

    console.log("\n-- everyone 40 or older, oldest first --");
    for (const u of users.find({ age: { $gte: 40 } }, { sort: [["age", -1]], limit: 10 })) {
      // Documents are plain objects with `unknown` values — the database
      // cannot know your schema. padEnd lines the names up.
      console.log(` ${String(u.name).padEnd(8)} ${u.age}`);
    }

    console.log("\n-- anyone tagged 'math' (arrays match element-wise) --");
    for (const u of users.find({ tags: "math" })) {
      console.log(" ", u.name);
    }

    console.log("\n-- dotted paths reach into nested objects --");
    const austin: Document | null = users.findOne({ "address.city": "Austin" });
    console.log(" ", austin?.name, "lives in Austin");

    console.log("\n-- $or, and counting --");
    const n = users.count({ $or: [{ age: { $lt: 40 } }, { age: { $gt: 60 } }] });
    console.log(` ${n} people are under 40 or over 60`);

    console.log("\n-- findOne on no match returns null --");
    console.log(" ", users.findOne({ name: "Nobody" }));

    // --- streaming ----------------------------------------------------------

    // iterFind pages under the hood, so JS-side memory stays flat no matter
    // how many documents match.
    console.log("\n-- streaming with iterFind --");
    let total = 0;
    for (const u of users.iterFind({}, { batch: 2 })) {
      total += Number(u.age);
    }
    console.log(`  average age: ${(total / users.count()).toFixed(1)}`);

    // --- errors -------------------------------------------------------------

    console.log("\n-- errors come back as exceptions --");
    try {
      users.find({ age: { $gtee: 30 } }); // typo in the operator
    } catch (err) {
      // `catch` gives you `unknown`, so narrow before using it.
      if (!(err instanceof NoSQLiteError)) throw err;
      console.log(" ", err.message);
    }

    console.log("\ncollections in this database:", db.collections());
  } finally {
    db.close();
  }

  console.log("\nDatabase file:", PATH);
  console.log("Trace of everything above is in", `${PATH}.trace`);
  console.log(`Both files are left in place — try: cat ${PATH}.trace`);
  console.log("Try: NOSQLITE_TRACE=verbose node examples/basic/basic.ts");
}

main();
