/**
 * Runs the shared conformance suite (../testdata) against nosqlite's public
 * TypeScript API. See ../../docs/testing.md for what a conformance test is
 * and why the fixtures live where they do — the Go suite
 * (../go/conformance_test.go) runs the exact same testdata/ and must agree
 * with this one.
 *
 * One `test(...)` per case, on purpose: each is a normal, named, breakpointable
 * test, not generated from a filesystem walk. Adding a case means adding one
 * more `test(...)` block below, the same ceremony as any other unit test.
 */

import { CaseRunner } from "./case-runner.ts";

const cases = new CaseRunner();

test("age-gte-41", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/comparison/age-gte-41");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("sort/single-key/age-asc-top5");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-desc-top5", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("sort/single-key/age-desc-top5");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5-name-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("sort/multi-key/age-asc-top5-name-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5-name-desc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("sort/multi-key/age-asc-top5-name-desc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-range-20-30", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/comparison/age-range-20-30");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-range-20-30-sort-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("combined/age-range-20-30-sort-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-gt-60-male", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/comparison/age-gt-60-male");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-gt-60-male-sort-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("combined/age-gt-60-male-sort-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-under-20-or-over-60", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/logical/age-under-20-or-over-60");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("not-age-gt-60", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/logical/not-age-gt-60");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("city-in", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/membership/city-in");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("city-nin", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/membership/city-nin");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("tag-eq-code", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/membership/tag-eq-code");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("tag-in-music-art", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/membership/tag-in-music-art");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("tag-all-code-music", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/membership/tag-all-code-music");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("name-substring-grace", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/regex/name-substring-grace");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("tag-substring-co", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/regex/tag-substring-co");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("has-tags", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/exists/has-tags");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("no-tags", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("filter/exists/no-tags");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-whole-document", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-whole-document");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-keeps-position", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-keeps-position");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-changes-match", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-changes-match");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-no-match-is-noop", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-no-match-is-noop");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-supplied-id", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-supplied-id");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-rejects-mismatched-id", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-rejects-mismatched-id");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("replace-twice-last-wins", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/replace/replace-twice-last-wins");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-removes-document", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-removes-document");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-no-match-is-noop", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-no-match-is-noop");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-only-first-match", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-only-first-match");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-many-removes-all-matches", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-many-removes-all-matches");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-many-empty-filter-empties-collection", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-many-empty-filter-empties-collection");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("delete-frees-id", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/delete-frees-id");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("insert-duplicate-id-rejected", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("mutate/delete/insert-duplicate-id-rejected");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-include-flat-fields", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/include-flat-fields");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-exclude-flat-fields", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/exclude-flat-fields");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-include-nested-path", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/include-nested-path");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-exclude-nested-path", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/exclude-nested-path");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-exclude-id", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/exclude-id");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-id-only", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/id-only");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("projection-sort-on-excluded-field", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("projection/sort-on-excluded-field");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});
