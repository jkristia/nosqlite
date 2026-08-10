/**
 * Low-level FFI bindings for libnosqlite.
 *
 * This is the only module in the package that knows koffi exists. Everything
 * above it deals in plain objects. It is the exact counterpart of the Python
 * package's `_lib.py`, against the exact same shared library.
 *
 * Node has no built-in FFI, so this uses `koffi` — a dependency-free-to-build
 * native module that ships prebuilt binaries, i.e. `npm install` and nothing
 * else. (Bun would use `bun:ffi` and Deno `Deno.dlopen`; both are built in.
 * The JSON-in/JSON-out convention on the C side means swapping the runtime
 * would only touch this file.)
 *
 * Two details matter here and nowhere else:
 *
 * 1. Every function is declared as returning `void *`, not `const char *`.
 *    With `char *` koffi helpfully copies the bytes into a JS string and
 *    throws the pointer away — which leaks the C allocation, because there is
 *    then no address left to hand to `nsq_free`. With `void *` we keep the
 *    address, read it with `koffi.decode`, and free it ourselves.
 *
 * 2. Every call goes through {@link call}, whose `try/finally` frees the
 *    returned string even if JSON parsing throws. That is the whole
 *    memory-safety story: no caller can forget.
 */

import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import koffi, { type LibraryHandle } from "koffi";

// koffi declares the type of a bound C function internally but does not export
// it, so name it here by asking what `lib.func()` returns.
type KoffiFunction = ReturnType<LibraryHandle["func"]>;

/** Raised for any error reported by the database. */
export class NoSQLiteError extends Error {
  override readonly name = "NoSQLiteError";
}

/** Any JSON object. Documents, filters and options are all just this. */
export type Json = Record<string, unknown>;

/** The platform-specific shared library filename. */
function libraryName(): string {
  if (process.platform === "darwin") return "libnosqlite.dylib";
  if (process.platform === "win32") return "nosqlite.dll";
  return "libnosqlite.so";
}

/**
 * Locate and load the shared library.
 *
 * `import.meta.url` is the URL of *this file*, which is how an ES module asks
 * "where do I live?" — there is no `__dirname` in a module.
 */
function loadLibrary(): LibraryHandle {
  const name = libraryName();
  const here = dirname(fileURLToPath(import.meta.url));

  const candidates = [
    // Next to this package, which is where `make build` copies it.
    join(here, name),
    // Convenient when running straight out of a source checkout: the Go build
    // writes the library into the Python package first, and the Makefile may
    // also have dropped one at the repo root.
    resolve(here, "..", "..", "python", "nosqlite", name),
    resolve(here, "..", "..", name),
  ];
  // An explicit override wins, which is handy for testing an unusual build.
  const override = process.env.NOSQLITE_LIBRARY;
  if (override) candidates.unshift(override);

  for (const path of candidates) {
    if (existsSync(path)) return koffi.load(resolve(path));
  }

  throw new NoSQLiteError(
    `could not find ${name}. Build it first:\n` +
      `    make build\n` +
      `(which runs: go build -buildmode=c-shared ` +
      `-o python/nosqlite/${name} ./capi)\n` +
      `Looked in: ${candidates.join(", ")}`,
  );
}

const lib = loadLibrary();

// Each signature is written as literal C. koffi parses these strings, so they
// must match capi/capi.go's exports exactly — a wrong argument type here is
// undefined behaviour, not a type error.
//
// The database handle is an int64 on the C side. JS numbers are IEEE doubles,
// so koffi hands back a plain number while the value fits in 2^53 (handles
// count up from 1; it fits) and a BigInt beyond that.
const nsqOpen = lib.func("void *nsq_open(const char *path, const char *opts)");
const nsqClose = lib.func("void *nsq_close(int64_t handle)");
const nsqInsert = lib.func("void *nsq_insert(int64_t handle, const char *coll, const char *doc)");
const nsqInsertMany = lib.func("void *nsq_insert_many(int64_t handle, const char *coll, const char *docs)");
const nsqFind = lib.func("void *nsq_find(int64_t handle, const char *coll, const char *query)");
const nsqCount = lib.func("void *nsq_count(int64_t handle, const char *coll, const char *filter)");
const nsqCollections = lib.func("void *nsq_collections(int64_t handle)");
const nsqFree = lib.func("void nsq_free(void *s)");

/** Call an exported function, parse its JSON reply, and free the string. */
function call(fn: KoffiFunction, ...args: unknown[]): Json {
  const ptr = fn(...args) as unknown;
  if (!ptr) throw new NoSQLiteError("library returned NULL");

  let payload: Json;
  try {
    // 'char' with a length of -1 means "read until the NUL terminator".
    const text = koffi.decode(ptr, "char", -1) as string;
    payload = JSON.parse(text) as Json;
  } finally {
    // Always, even if JSON.parse threw.
    nsqFree(ptr);
  }

  if (typeof payload.error === "string") {
    // Note: nsq_insert_many can report an error *and* the ids that did land.
    // Like the Python binding, we surface the error and drop the partial ids;
    // see Collection.insertMany's doc comment.
    throw new NoSQLiteError(payload.error);
  }
  return payload;
}

// --- thin wrappers, one per export -----------------------------------------

export function openDatabase(path: string, options: Json = {}): number {
  return Number(call(nsqOpen, path, JSON.stringify(options)).handle);
}

export function closeDatabase(handle: number): void {
  call(nsqClose, handle);
}

export function insert(handle: number, collection: string, document: Json): string {
  return call(nsqInsert, handle, collection, JSON.stringify(document)).id as string;
}

export function insertMany(handle: number, collection: string, documents: Json[]): string[] {
  return call(nsqInsertMany, handle, collection, JSON.stringify(documents)).ids as string[];
}

export function find(handle: number, collection: string, query: Json): Json[] {
  return call(nsqFind, handle, collection, JSON.stringify(query)).docs as Json[];
}

export function count(handle: number, collection: string, filter: Json | null): number {
  return Number(call(nsqCount, handle, collection, JSON.stringify(filter ?? {})).count);
}

export function collections(handle: number): string[] {
  return call(nsqCollections, handle).names as string[];
}
