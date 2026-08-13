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
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-gte-41");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-asc-top5");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-desc-top5", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-desc-top5");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5-name-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-asc-top5-name-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-asc-top5-name-desc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-asc-top5-name-desc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-range-20-30", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-range-20-30");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-range-20-30-sort-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-range-20-30-sort-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-gt-60-male", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-gt-60-male");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});

test("age-gt-60-male-sort-asc", () => {
  const { got, expected, gotDocs, expectedDocs } = cases.run("age-gt-60-male-sort-asc");
  expect(got).toEqual(expected);
  expect(gotDocs).toEqual(expectedDocs);
});
