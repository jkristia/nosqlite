/**
 * nosqlite — an embedded document store, in one file, with a Mongo-ish API.
 *
 * It should feel like the `mongodb` driver if you have used it, and like
 * `better-sqlite3` if you have not: every call is synchronous, because it goes
 * straight into a library linked into this process. There is no socket, no
 * event loop hop, and so nothing to `await`.
 *
 * ```ts
 * import { Database } from "nosqlite";
 *
 * const db = new Database("./demo.nsq", { trace: "all" });
 * try {
 *   const users = db.collection("users");
 *
 *   users.insert({ name: "Ada", age: 36, tags: ["math"] });
 *   users.insertMany([
 *     { name: "Grace", age: 45 },
 *     { name: "Alan", age: 41 },
 *   ]);
 *
 *   for (const u of users.find({ age: { $gte: 40 } }, { sort: [["age", -1]], limit: 10 })) {
 *     console.log(u.name, u.age);
 *   }
 *
 *   console.log(users.findOne({ name: "Ada" }));      // object, or null
 *   console.log(users.count({ age: { $gte: 40 } }));  // 2
 *   console.log(users.count());                       // 3
 *   console.log(db.collections());                    // ["users"]
 * } finally {
 *   db.close();
 * }
 * ```
 *
 * The database file is the path you pass in. A trace file appears beside it as
 * `<path>.trace` when tracing is on — either via `trace:` above, or by setting
 * `NOSQLITE_TRACE=all` in the environment, which needs no code change at all.
 */

import * as ffi from "./ffi.ts";
import { NoSQLiteError, type Json } from "./ffi.ts";

export { NoSQLiteError };
export type { Json };

/** A stored document. `_id` is always present on documents that come back. */
export type Document = Json;

/** A Mongo-dialect filter, e.g. `{ age: { $gte: 30 } }`. */
export type Filter = Json;

/** One sort key: `["age", -1]` is descending, `["age", 1]` ascending. */
export type SortKey = [field: string, direction: 1 | -1];

/**
 * `find()` applies this limit when the caller gives none.
 *
 * Without it, one absent-minded `find({})` over a large collection
 * materialises the whole thing three times over — as a Go slice, as one giant
 * C string, and as a JS array. Surprising someone with a truncated array is
 * bad; surprising them with 4 GB of RSS is worse, so `find` throws rather than
 * truncating silently. Pass `limit: 0` to genuinely ask for everything.
 */
export const DEFAULT_LIMIT = 1000;

/** Options for {@link Database}. */
export interface DatabaseOptions {
  /** `"always"` (default, durable) or `"never"` (fast bulk loading; flushed on close). */
  sync?: "always" | "never";
  /**
   * `"writes"`, `"all"` or `"verbose"` to enable the trace file. The
   * `NOSQLITE_TRACE` environment variable overrides this, and works without
   * touching the code.
   */
  trace?: "off" | "writes" | "all" | "verbose";
  /** Where to write the trace; default `<path>.trace`. */
  traceFile?: string;
  /** Skip checksum verification while replaying the file. */
  fastOpen?: boolean;
}

/** Options for {@link Collection.find}. */
export interface FindOptions {
  /** Sort keys, applied in order. */
  sort?: SortKey[];
  /** Documents to drop before returning any. */
  skip?: number;
  /**
   * Maximum documents to return. Omitted applies {@link DEFAULT_LIMIT} and
   * throws if that limit is actually hit; `0` means no limit at all.
   */
  limit?: number;
}

/** Options for {@link Collection.iterFind}. */
export interface IterFindOptions {
  sort?: SortKey[];
  skip?: number;
  /** How many documents to fetch per underlying call. */
  batch?: number;
}

/** An open database file. Close it in a `finally` block. */
export class Database {
  readonly path: string;
  // `#handle` is a real private field: nothing outside this class body can
  // reach it, not even other code in this module. null means closed.
  #handle: number | null;

  /** Open (or create) the database at `path`. */
  constructor(path: string, options: DatabaseOptions = {}) {
    // The C side takes snake_case keys, so translate rather than passing the
    // caller's object through — that keeps the JS API idiomatic and the wire
    // format stable.
    const wire: Json = { sync: options.sync ?? "always" };
    if (options.trace !== undefined) wire.trace = options.trace;
    if (options.traceFile !== undefined) wire.trace_file = options.traceFile;
    if (options.fastOpen) wire.fast_open = true;

    this.path = path;
    this.#handle = ffi.openDatabase(path, wire);
  }

  // -- lifecycle ------------------------------------------------------------

  /** Close the database. Safe to call more than once. */
  close(): void {
    if (this.#handle !== null) {
      const handle = this.#handle;
      // Clear it first, so a throw below cannot leave a half-closed database
      // that a later close() would try to close twice.
      this.#handle = null;
      ffi.closeDatabase(handle);
    }
  }

  /**
   * Lets newer runtimes write `using db = new Database(...)`, which closes at
   * the end of the block the way `defer db.Close()` does in Go. On runtimes
   * without `using`, this is simply never called — use `try/finally`.
   */
  [Symbol.dispose](): void {
    this.close();
  }

  /** The raw C handle. Throws once the database is closed. */
  get handle(): number {
    if (this.#handle === null) throw new NoSQLiteError("database is closed");
    return this.#handle;
  }

  // -- collections ----------------------------------------------------------

  /** Return a collection, creating it if it does not exist. */
  collection(name: string): Collection {
    return new Collection(this, name);
  }

  /** Names of every collection, sorted. */
  collections(): string[] {
    return ffi.collections(this.handle);
  }
}

/** A named group of documents inside a {@link Database}. */
export class Collection {
  readonly database: Database;
  readonly name: string;

  constructor(database: Database, name: string) {
    this.database = database;
    this.name = name;
  }

  // -- writing --------------------------------------------------------------

  /**
   * Store one document and return its `_id`.
   *
   * If the document has no `_id`, one is generated. Your object is not
   * modified — keep the return value if you want the id.
   */
  insert(document: Document): string {
    return ffi.insert(this.database.handle, this.name, document);
  }

  /**
   * Store several documents with a single write and a single flush.
   *
   * Not atomic: on a crash a prefix of the batch can survive. If the batch
   * fails part-way this throws, and the ids that were written are lost to the
   * caller — insert one at a time if you need to know exactly how far it got.
   */
  insertMany(documents: readonly Document[]): string[] {
    return ffi.insertMany(this.database.handle, this.name, [...documents]);
  }

  /**
   * Overwrite the first document matching `filter`, and return how many were
   * replaced — `1`, or `0` when nothing matched.
   *
   * `document` replaces the stored one entirely: fields it omits are gone,
   * this is not a merge. At most one document is touched, so a too-broad
   * filter cannot rewrite the collection.
   *
   * `_id` is immutable. The replacement keeps the matched document's id, and
   * you may include the same `_id` in `document` but not a different one —
   * that throws {@link NoSQLiteError}.
   *
   * That check is worth reaching for. An `_id` in `document` is not a second
   * way to pick the document — `filter` always picks — it is an assertion that
   * `filter` found the one you meant, and it costs nothing:
   *
   * ```ts
   * // Meant document 7, but this filter matches document 8.
   * users.replace({ name: "Emma Osei" }, { _id: "7", age: 33 });
   * // NoSQLiteError: nosqlite: _id is immutable: the filter matched document
   * // "8", but the replacement carries _id "7"; one of them is wrong
   * ```
   *
   * Leave the `_id` out and that same typo overwrites document 8 silently.
   */
  replace(filter: Filter, document: Document): number {
    return ffi.replace(this.database.handle, this.name, filter, document);
  }

  // -- reading --------------------------------------------------------------

  /**
   * Return the documents matching `filter`. An omitted filter matches
   * everything.
   */
  find(filter: Filter = {}, options: FindOptions = {}): Document[] {
    const explicitLimit = options.limit !== undefined;
    const limit = options.limit ?? DEFAULT_LIMIT;

    const docs = ffi.find(this.database.handle, this.name, {
      filter,
      sort: encodeSort(options.sort),
      skip: options.skip ?? 0,
      limit,
    });

    if (!explicitLimit && docs.length === DEFAULT_LIMIT) {
      throw new NoSQLiteError(
        `find() hit the default limit of ${DEFAULT_LIMIT} documents. ` +
          `Pass limit: N for a bigger page, limit: 0 for everything, or use ` +
          `iterFind() to stream results without holding them all in memory.`,
      );
    }
    return docs;
  }

  /** Return the first matching document, or `null`. */
  findOne(filter: Filter = {}): Document | null {
    const docs = this.find(filter, { limit: 1 });
    return docs[0] ?? null;
  }

  /**
   * Yield matching documents one at a time, paging under the hood.
   *
   * JS-side memory stays flat regardless of how many documents match, which is
   * what you want over a large result set. Note that each page is a fresh
   * query: with no sort the pages are stable because the underlying store is
   * append-only, but a concurrent writer can add documents that a later page
   * then includes.
   *
   * The `*` makes this a generator — each `yield` hands one document to the
   * `for...of` loop and pauses here until the loop asks for the next one.
   */
  *iterFind(filter: Filter = {}, options: IterFindOptions = {}): Generator<Document> {
    const batch = options.batch ?? 1000;
    if (batch <= 0) throw new RangeError("batch must be positive");

    let offset = options.skip ?? 0;
    for (;;) {
      const page = this.find(filter, { sort: options.sort, skip: offset, limit: batch });
      if (page.length === 0) return;
      yield* page;
      if (page.length < batch) return;
      offset += page.length;
    }
  }

  /** Count matching documents. An empty filter is answered instantly. */
  count(filter: Filter | null = null): number {
    return ffi.count(this.database.handle, this.name, filter);
  }
}

/** Turn `[["age", -1]]` into the wire format `[{ field, desc }]`. */
function encodeSort(sort: SortKey[] | undefined): Array<{ field: string; desc: boolean }> {
  if (!sort) return [];
  return sort.map(([field, direction]) => {
    // TypeScript already rejects a bad direction at compile time, but the type
    // annotations are erased at runtime and plain JS callers exist, so check.
    if (direction !== 1 && direction !== -1) {
      throw new RangeError(
        `sort direction must be 1 (ascending) or -1 (descending), got ${String(direction)}`,
      );
    }
    return { field, desc: direction === -1 };
  });
}
