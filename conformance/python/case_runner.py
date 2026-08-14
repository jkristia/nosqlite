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

from nosqlite import Database  # noqa: E402


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

            # query.json's limit follows the fixture convention (0/omitted =
            # no limit), but find()'s own default of None applies
            # DEFAULT_LIMIT and raises if that limit is hit. Always pass the
            # limit explicitly so "no limit" in the fixture means "no limit"
            # here too.
            got = docs.find(
                query.get("filter") or {},
                sort=_to_sort_tuples(query.get("sort")),
                skip=query.get("skip", 0),
                limit=query.get("limit", 0) or 0,
            )

        result = CaseResult(
            got=[d["_id"] for d in got],
            expected=expected["ids"],
        )
        fields = expected.get("fields")
        if fields:
            result.got_docs = [{f: d.get(f) for f in fields} for d in got]
            result.expected_docs = expected.get("docs") or []
        return result
