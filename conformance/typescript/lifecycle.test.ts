/**
 * A different shape of test than conformance.test.ts: instead of one fresh,
 * throwaway database per case (deliberate isolation — see case-runner.ts and
 * docs/testing.md), this file opens ONE database in `beforeAll`, seeds it
 * once, and runs several queries against that same live handle — closer to
 * how a real process actually uses nosqlite (open once at startup, query
 * many times) than "open, insert, query, close" repeated per assertion.
 *
 * The last test closes that handle and opens a brand new one against the
 * same on-disk path, to prove a freshly-opened database replays correctly
 * and answers the same queries — the thing the shared-handle tests above it
 * can't exercise, since they never let the file go cold.
 *
 * Reuses the "people" dataset and a few queries whose expected ids are
 * already verified against the real engine in
 * ../testdata/cases/filter/comparison/age-gt-60-male and
 * ../testdata/cases/filter/membership/city-in.
 *
 * Also seeds a second collection ("products", inline — not one of the shared
 * conformance datasets) alongside "docs" on the same handle, to prove two
 * collections don't cross-contaminate reads or writes. That's the one thing
 * conformance.test.ts structurally can't exercise, since each of its cases
 * gets its own single-collection database.
 */

import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Database, type Document } from "../../typescript/nosqlite/index.ts";

const testdataDir = resolve(dirname(fileURLToPath(import.meta.url)), "..", "testdata");

function readJSONL<T>(path: string): T[] {
  return readFileSync(path, "utf8")
    .split("\n")
    .filter((line) => line.trim().length > 0)
    .map((line) => JSON.parse(line) as T);
}

const dbDir = mkdtempSync(join(tmpdir(), "nosqlite-lifecycle-"));
const dbPath = join(dbDir, "lifecycle.nsq");

// Small, inline — not one of the shared conformance datasets, since this
// exists to prove collection isolation on a shared handle, not query/matcher
// parity. Field names deliberately don't overlap with the people dataset.
const products: Document[] = [
  { _id: "p1", sku: "WIDGET-1", price: 9.99, category: "tools" },
  { _id: "p2", sku: "WIDGET-2", price: 19.99, category: "tools" },
  { _id: "p3", sku: "GADGET-1", price: 29.99, category: "electronics" },
];

let db: Database;

beforeAll(() => {
  const dataset = readJSONL<Document>(join(testdataDir, "datasets", "people.jsonl"));
  db = new Database(dbPath);
  db.collection("docs").insertMany(dataset);
  db.collection("products").insertMany(products);
});

afterAll(() => {
  db.close();
  rmSync(dbDir, { recursive: true, force: true });
});

test("age-gt-60-male, against the shared handle", () => {
  const got = db.collection("docs").find({ age: { $gt: 60 }, gender: "male" });
  expect(got.map((d) => d._id)).toEqual(["24", "25", "47", "48", "49"]);
});

test("city-in, against the same shared handle, no reinsertion needed", () => {
  const got = db.collection("docs").find({ "address.city": { $in: ["London", "Paris", "Toronto"] } });
  expect(got.map((d) => d._id)).toEqual(["1", "3", "5", "13", "15", "17", "25", "29", "37", "39", "41", "49"]);
});

test("age-gte-41 sorted, still the same handle, third query in a row", () => {
  const got = db.collection("docs").find({ age: { $gte: 41 } }, { sort: [["age", 1]] });
  expect(got.map((d) => d._id)).toEqual([
    "12", "37", "13", "38", "14", "39", "15", "40", "16", "41", "17", "42", "18", "43", "19", "44", "20", "45", "21", "46", "22", "47", "23", "48", "24", "49", "25", "50",
  ]);
});

test("a second collection on the same handle stays isolated from the first", () => {
  expect(db.collections()).toEqual(["docs", "products"]);

  const tools = db.collection("products").find({ category: "tools" });
  expect(tools.map((d) => d._id)).toEqual(["p1", "p2"]);
  expect(db.collection("products").count()).toBe(products.length);

  // A filter shaped like a "docs" query against "products" just matches
  // nothing there — no cross-collection leakage, no error.
  expect(db.collection("products").find({ age: { $gt: 60 } })).toEqual([]);

  // "docs" itself is unaffected by the writes and reads against "products".
  const got = db.collection("docs").find({ age: { $gt: 60 }, gender: "male" });
  expect(got.map((d) => d._id)).toEqual(["24", "25", "47", "48", "49"]);
});

test("reopening a fresh handle against the same on-disk file still answers correctly", () => {
  db.close();
  db = new Database(dbPath);

  expect(db.collections()).toEqual(["docs", "products"]);

  const got = db.collection("docs").find({ age: { $gt: 60 }, gender: "male" });
  expect(got.map((d) => d._id)).toEqual(["24", "25", "47", "48", "49"]);

  const tools = db.collection("products").find({ category: "tools" });
  expect(tools.map((d) => d._id)).toEqual(["p1", "p2"]);
});
