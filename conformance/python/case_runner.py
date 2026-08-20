"""Loads and runs one conformance case against nosqlite's public Python API.

Mirrors conformance/go/conformance_test.go's runCase and
conformance/typescript/case-runner.ts's CaseRunner.run. See
../../docs/testing.md for why this suite exists.
"""

from __future__ import annotations

import json
import os
import sys
import tempfile
from dataclasses import dataclass
from typing import Any

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.abspath(os.path.join(_HERE, "..", ".."))
_TESTDATA_DIR = os.path.join(_REPO_ROOT, "conformance", "testdata")

sys.path.insert(0, os.path.join(_REPO_ROOT, "python"))

from nosqlite import Database, NoSQLiteError  # noqa: E402


@dataclass
class CaseResult:
    got: list[str]
    expected: list[str]
    got_docs: list[dict[str, Any]] | None = None
    expected_docs: list[dict[str, Any]] | None = None


def discover_case_names() -> list[str]:
    """Finds every case directory (one containing query.json) under testdata/cases/."""
    cases_dir = os.path.join(_TESTDATA_DIR, "cases")
    names = []
    for root, _dirs, files in os.walk(cases_dir):
        if "query.json" in files:
            rel = os.path.relpath(root, cases_dir).replace(os.sep, "/")
            names.append(rel)
    return sorted(names)


def _read_json(path: str) -> Any:
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def _read_jsonl(path: str) -> list[dict[str, Any]]:
    docs = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                docs.append(json.loads(line))
    return docs


def _to_sort_tuples(sort: list[dict[str, Any]] | None) -> list[tuple[str, int]]:
    return [(s["field"], -1 if s.get("desc") else 1) for s in (sort or [])]


def _perform_mutation(docs: Any, op: str, mutation: dict[str, Any]) -> int:
    """Performs the write itself and returns the count it produced.

    Kept separate so ``_apply_mutation``'s ``try`` wraps nothing but the call --
    an op this runner does not implement is rejected before we get here.
    """
    if op == "insert":
        # insert() returns an id rather than a count; a successful one wrote
        # exactly one document, so report 1 and let ``matched`` mean the same
        # thing it means for every other op.
        docs.insert(mutation["document"])
        return 1
    if op == "replace":
        return docs.replace(mutation.get("filter") or {}, mutation["document"])
    if op == "delete":
        return docs.delete(mutation.get("filter") or {})
    return docs.delete_many(mutation.get("filter") or {})


def _apply_mutation(docs: Any, i: int, mutation: dict[str, Any]) -> None:
    """Performs one of a case's mutations.

    An unknown op raises rather than being skipped: a fixture naming an op this
    runner does not implement would otherwise pass while testing nothing. That
    check comes before the call so it cannot be mistaken for the failure an
    ``error`` assertion expects.
    """
    op = mutation["op"]
    if op not in ("insert", "replace", "delete", "delete_many"):
        raise AssertionError(f"mutations[{i}]: unknown op {op!r}")

    want_error = mutation.get("error")
    try:
        matched = _perform_mutation(docs, op, mutation)
    except NoSQLiteError as exc:
        if not want_error:
            raise
        if want_error not in str(exc):
            raise AssertionError(
                f"mutations[{i}] ({op}): error {str(exc)!r} does not contain {want_error!r}"
            ) from exc
        return

    if want_error:
        raise AssertionError(
            f"mutations[{i}] ({op}): want an error containing {want_error!r}, "
            f"got none (matched {matched})"
        )

    want = mutation.get("matched")
    if want is not None and matched != want:
        raise AssertionError(
            f"mutations[{i}] ({op}) matched {matched} documents, want {want}"
        )


def run_case(name: str) -> CaseResult:
    case_dir = os.path.join(_TESTDATA_DIR, "cases", name)
    query = _read_json(os.path.join(case_dir, "query.json"))
    expected = _read_json(os.path.join(case_dir, "expected.json"))
    dataset = _read_jsonl(os.path.join(_TESTDATA_DIR, "datasets", f"{query['dataset']}.jsonl"))

    with tempfile.TemporaryDirectory(prefix="nosqlite-conformance-") as tmp_dir:
        db_path = os.path.join(tmp_dir, "conformance.nsq")
        with Database(db_path) as db:
            docs = db.collection("docs")
            docs.insert_many(dataset)

            for i, mutation in enumerate(query.get("mutations") or []):
                _apply_mutation(docs, i, mutation)

            # query.json's limit follows the fixture convention (0/omitted =
            # no limit), but find()'s own default of None applies
            # DEFAULT_LIMIT and raises if that limit is hit. Always pass the
            # limit explicitly so "no limit" in the fixture means "no limit"
            # here too.
            got = docs.find(
                query.get("filter") or {},
                projection=query.get("projection") or None,
                sort=_to_sort_tuples(query.get("sort")),
                skip=query.get("skip", 0),
                limit=query.get("limit", 0) or 0,
            )

        # A missing ``ids`` means the query projected _id away, which is the
        # only case allowed to omit it; ``docs`` then carries the assertion.
        ids = expected.get("ids")
        result = CaseResult(
            got=[d["_id"] for d in got] if ids is not None else [],
            expected=ids if ids is not None else [],
        )
        fields = expected.get("fields")
        if fields:
            result.got_docs = [{f: d.get(f) for f in fields} for d in got]
            result.expected_docs = expected.get("docs") or []
        elif expected.get("docs") is not None:
            # No ``fields`` to narrow by, so the documents are compared whole --
            # the engine's own projection is what produced their shape.
            result.got_docs = got
            result.expected_docs = expected["docs"]
        return result
