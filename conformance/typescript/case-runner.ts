/**
 * Loads and runs one conformance case against nosqlite's public TypeScript
 * API. See ../../docs/testing.md §4a for why this is a class rather than
 * standalone functions, and why conformance.test.ts writes one explicit
 * `test(...)` per case instead of generating them from the filesystem.
 */

import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { Database, type Document, type Filter, type SortKey } from "../../typescript/nosqlite/index.ts";

/** One sort key as written in query.json — see query.schema.json. */
interface CaseSortKey {
  field: string;
  desc?: boolean;
}

/**
 * A case's query.json. Mirrors nosqlite's Query, but names its dataset
 * instead of embedding it, so many cases can share one dataset file.
 */
interface CaseQuery {
  dataset: string;
  filter?: Filter;
  sort?: CaseSortKey[];
  skip?: number;
  limit?: number;
}

/**
 * A case's expected.json: the _id list every binding must produce, in order.
 *
 * `fields`/`docs` are optional: nosqlite has no projection support, so they
 * don't change the query. They let a case narrow the full documents find()
 * already returns down to a few fields, purely so expected.json stays
 * readable instead of embedding whole documents.
 */
interface CaseExpected {
  ids: string[];
  fields?: string[];
  docs?: Record<string, unknown>[];
}

/** What running a case actually produced, alongside what it should have. */
export interface CaseResult {
  got: string[];
  expected: string[];
  /** Present only when the case's expected.json sets `fields`. */
  gotDocs?: Record<string, unknown>[];
  expectedDocs?: Record<string, unknown>[];
}

/**
 * Runs a named case from ../testdata/cases against a fresh, temporary
 * database. One instance is reused across every `test(...)` in
 * conformance.test.ts — it holds no per-case state, only the path to
 * ../testdata.
 */
export class CaseRunner {
  private readonly testdataDir: string;

  constructor() {
    const here = dirname(fileURLToPath(import.meta.url));
    this.testdataDir = resolve(here, "..", "testdata");
  }

  /**
   * Runs the case named by `name` (its path relative to testdata/cases/,
   * e.g. "age-gte" or "filter/comparison/gte") and returns the ids Find()
   * actually produced alongside the ids expected.json says it should have.
   *
   * Returning both rather than asserting here keeps the `expect(...)` call
   * in the test body, where a debugger stopped on it can inspect both
   * lists directly.
   */
  run(name: string): CaseResult {
    const dir = join(this.testdataDir, "cases", name);
    const query = this.readJSON<CaseQuery>(join(dir, "query.json"));
    const expected = this.readJSON<CaseExpected>(join(dir, "expected.json"));
    const dataset = this.readJSONL<Document>(
      join(this.testdataDir, "datasets", `${query.dataset}.jsonl`),
    );

    const dbDir = mkdtempSync(join(tmpdir(), "nosqlite-conformance-"));
    const db = new Database(join(dbDir, "conformance.nsq"));
    try {
      const docs = db.collection("docs");
      docs.insertMany(dataset);

      const got = docs.find(query.filter ?? {}, {
        sort: this.toSortKeys(query.sort),
        skip: query.skip ?? 0,
        // query.json's `limit` follows Query's convention (0/omitted = no
        // limit) — pass 0 explicitly rather than leaving it undefined,
        // since find() defaults an *omitted* limit to DEFAULT_LIMIT instead.
        limit: query.limit ?? 0,
      });

      const result: CaseResult = { got: got.map((d) => d._id as string), expected: expected.ids };
      if (expected.fields && expected.fields.length > 0) {
        result.gotDocs = got.map((d) => this.projectFields(d, expected.fields!));
        result.expectedDocs = expected.docs ?? [];
      }
      return result;
    } finally {
      db.close();
      rmSync(dbDir, { recursive: true, force: true });
    }
  }

  private readJSON<T>(path: string): T {
    return JSON.parse(readFileSync(path, "utf8")) as T;
  }

  /** Narrows doc down to fields, so a case can check a few fields without expected.json embedding the whole document. */
  private projectFields(doc: Document, fields: string[]): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const f of fields) {
      out[f] = (doc as Record<string, unknown>)[f];
    }
    return out;
  }

  /** Reads a dataset file: one JSON document per line. */
  private readJSONL<T>(path: string): T[] {
    return readFileSync(path, "utf8")
      .split("\n")
      .filter((line) => line.trim().length > 0)
      .map((line) => JSON.parse(line) as T);
  }

  private toSortKeys(sort: CaseSortKey[] | undefined): SortKey[] {
    return (sort ?? []).map((s): SortKey => [s.field, s.desc ? -1 : 1]);
  }
}
