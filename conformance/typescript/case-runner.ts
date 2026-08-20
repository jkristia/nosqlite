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

import {
  Collection,
  Database,
  type Document,
  type Filter,
  type Projection,
  type SortKey,
} from "../../typescript/nosqlite/index.ts";

/** One sort key as written in query.json — see query.schema.json. */
interface CaseSortKey {
  field: string;
  desc?: boolean;
}

/**
 * One write applied between seeding the dataset and running the query, which
 * is how a case covers write behaviour: the query afterwards observes what the
 * write did.
 */
interface CaseMutation {
  op: "insert" | "replace" | "delete" | "delete_many";
  filter?: Filter;
  /** Required by `insert` and `replace`, unused by the deletes. */
  document?: Document;
  /** The count the operation must return. Absent means "don't check". */
  matched?: number;
  /**
   * When set, the operation must fail with a message containing this text.
   * Mutually exclusive with `matched` — a failed op has no count.
   */
  error?: string;
}

/**
 * A case's query.json. Mirrors nosqlite's Query, but names its dataset
 * instead of embedding it, so many cases can share one dataset file.
 */
interface CaseQuery {
  dataset: string;
  mutations?: CaseMutation[];
  filter?: Filter;
  projection?: Projection;
  sort?: CaseSortKey[];
  skip?: number;
  limit?: number;
}

/**
 * A case's expected.json: what every binding must produce, in order.
 *
 * `docs` is compared in full when `fields` is absent, which is how a case with
 * a `projection` in its query.json checks the shape the engine produced. With
 * `fields` set, the runner instead narrows the documents find() returned down
 * to those field names first — client-side and flat, purely so expected.json
 * stays readable for cases that query whole documents.
 *
 * `ids` may be absent, and only then: a projection that drops `_id` leaves no
 * ids to check.
 */
interface CaseExpected {
  ids?: string[];
  fields?: string[];
  docs?: Record<string, unknown>[];
}

/** What running a case actually produced, alongside what it should have. */
export interface CaseResult {
  got: string[];
  expected: string[];
  /** Present only when the case's expected.json sets `docs`. */
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

      (query.mutations ?? []).forEach((m, i) => this.applyMutation(docs, i, m));

      const got = docs.find(query.filter ?? {}, {
        projection: query.projection,
        sort: this.toSortKeys(query.sort),
        skip: query.skip ?? 0,
        // query.json's `limit` follows Query's convention (0/omitted = no
        // limit) — pass 0 explicitly rather than leaving it undefined,
        // since find() defaults an *omitted* limit to DEFAULT_LIMIT instead.
        limit: query.limit ?? 0,
      });

      // An absent `ids` means the query projected _id away, which is the only
      // case allowed to omit it; `docs` then carries the assertion.
      const result: CaseResult =
        expected.ids === undefined
          ? { got: [], expected: [] }
          : { got: got.map((d) => d._id as string), expected: expected.ids };

      if (expected.fields && expected.fields.length > 0) {
        result.gotDocs = got.map((d) => this.projectFields(d, expected.fields!));
        result.expectedDocs = expected.docs ?? [];
      } else if (expected.docs !== undefined) {
        // No `fields` to narrow by, so the documents are compared whole — the
        // engine's own projection is what produced their shape.
        result.gotDocs = got as Record<string, unknown>[];
        result.expectedDocs = expected.docs;
      }
      return result;
    } finally {
      db.close();
      rmSync(dbDir, { recursive: true, force: true });
    }
  }

  /**
   * Performs one of a case's mutations. An unknown op throws rather than being
   * skipped: a fixture naming an op this runner does not implement would
   * otherwise pass while testing nothing. That check sits outside the try
   * below, so it cannot be mistaken for the failure an `error` assertion
   * expects.
   */
  private applyMutation(docs: Collection, i: number, m: CaseMutation): void {
    if (!["insert", "replace", "delete", "delete_many"].includes(m.op)) {
      throw new Error(`mutations[${i}]: unknown op ${String(m.op)}`);
    }

    let matched: number;
    try {
      matched = this.performMutation(docs, m);
    } catch (e) {
      if (!m.error) throw e;
      const message = (e as Error).message;
      if (!message.includes(m.error)) {
        throw new Error(
          `mutations[${i}] (${m.op}): error "${message}" does not contain "${m.error}"`,
        );
      }
      return;
    }

    if (m.error !== undefined) {
      throw new Error(
        `mutations[${i}] (${m.op}): want an error containing "${m.error}", got none (matched ${matched})`,
      );
    }
    if (m.matched !== undefined && matched !== m.matched) {
      throw new Error(
        `mutations[${i}] (${m.op}) matched ${matched} documents, want ${m.matched}`,
      );
    }
  }

  /**
   * Performs the write itself and returns the count it produced, so
   * applyMutation's try/catch wraps nothing but the call — an op this runner
   * does not implement is rejected before we get here.
   */
  private performMutation(docs: Collection, m: CaseMutation): number {
    switch (m.op) {
      case "insert":
        // insert() returns an id rather than a count; a successful one wrote
        // exactly one document, so report 1 and let `matched` mean the same
        // thing it means for every other op.
        docs.insert(m.document!);
        return 1;
      case "replace":
        return docs.replace(m.filter ?? {}, m.document!);
      case "delete":
        return docs.delete(m.filter ?? {});
      default:
        return docs.deleteMany(m.filter ?? {});
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
