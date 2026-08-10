"""Low-level ctypes bindings for libnosqlite.

This is the only module in the package that knows ctypes exists. Everything
above it deals in dicts.

Two details matter here and nowhere else:

1. ``restype`` is ``c_void_p``, not ``c_char_p``. With ``c_char_p`` ctypes
   helpfully copies the bytes into a Python ``bytes`` object and throws the
   pointer away -- which leaks the C allocation, because there is then no
   address left to hand to ``nsq_free``. With ``c_void_p`` we keep the address,
   read it with ``ctypes.string_at``, and free it ourselves.

2. Every call goes through ``_call``, whose ``try/finally`` frees the returned
   string even if JSON decoding raises. That is the whole memory-safety story:
   no caller can forget.
"""

from __future__ import annotations

import ctypes
import json
import os
import sys
from typing import Any


class NoSQLiteError(Exception):
    """Raised for any error reported by the database."""


def _library_name() -> str:
    """The platform-specific shared library filename."""
    if sys.platform == "darwin":
        return "libnosqlite.dylib"
    if sys.platform == "win32":
        return "nosqlite.dll"
    return "libnosqlite.so"


def _load_library() -> ctypes.CDLL:
    """Locate and load the shared library that sits next to this package."""
    name = _library_name()
    here = os.path.dirname(os.path.abspath(__file__))

    candidates = [
        os.path.join(here, name),
        # Convenient when running straight out of a source checkout, where the
        # Makefile may have dropped the library at the repo root.
        os.path.join(here, "..", "..", name),
    ]
    # An explicit override wins, which is handy for testing an unusual build.
    override = os.environ.get("NOSQLITE_LIBRARY")
    if override:
        candidates.insert(0, override)

    for path in candidates:
        if os.path.exists(path):
            return ctypes.CDLL(os.path.abspath(path))

    raise NoSQLiteError(
        f"could not find {name}. Build it first:\n"
        f"    make build\n"
        f"(which runs: go build -buildmode=c-shared "
        f"-o python/nosqlite/{name} ./capi)\n"
        f"Looked in: {', '.join(candidates)}"
    )


_lib = _load_library()

# Declaring argtypes/restype is not optional: without them ctypes guesses, and
# on 64-bit platforms it guesses that pointers are 32-bit ints, which corrupts
# every returned address.
_c_str = ctypes.c_char_p
_c_ptr = ctypes.c_void_p
_c_ll = ctypes.c_longlong

_lib.nsq_open.argtypes = [_c_str, _c_str]
_lib.nsq_open.restype = _c_ptr

_lib.nsq_close.argtypes = [_c_ll]
_lib.nsq_close.restype = _c_ptr

_lib.nsq_insert.argtypes = [_c_ll, _c_str, _c_str]
_lib.nsq_insert.restype = _c_ptr

_lib.nsq_insert_many.argtypes = [_c_ll, _c_str, _c_str]
_lib.nsq_insert_many.restype = _c_ptr

_lib.nsq_find.argtypes = [_c_ll, _c_str, _c_str]
_lib.nsq_find.restype = _c_ptr

_lib.nsq_count.argtypes = [_c_ll, _c_str, _c_str]
_lib.nsq_count.restype = _c_ptr

_lib.nsq_collections.argtypes = [_c_ll]
_lib.nsq_collections.restype = _c_ptr

_lib.nsq_free.argtypes = [_c_ptr]
_lib.nsq_free.restype = None


def _call(fn, *args) -> dict[str, Any]:
    """Call an exported function, decode its JSON reply, and free the string."""
    ptr = fn(*args)
    if not ptr:
        raise NoSQLiteError("library returned NULL")
    try:
        payload = json.loads(ctypes.string_at(ptr).decode("utf-8"))
    finally:
        # Always, even if json.loads raised.
        _lib.nsq_free(ptr)
    if "error" in payload:
        raise NoSQLiteError(payload["error"])
    return payload


def _utf8(s: str | None) -> bytes | None:
    """Encode a Python string for the C boundary. None passes through as NULL."""
    return None if s is None else s.encode("utf-8")


# --- thin wrappers, one per export -----------------------------------------


def open_database(path: str, options: dict[str, Any] | None = None) -> int:
    reply = _call(_lib.nsq_open, _utf8(path), _utf8(json.dumps(options or {})))
    return int(reply["handle"])


def close_database(handle: int) -> None:
    _call(_lib.nsq_close, _c_ll(handle))


def insert(handle: int, collection: str, document: dict[str, Any]) -> str:
    reply = _call(
        _lib.nsq_insert, _c_ll(handle), _utf8(collection), _utf8(json.dumps(document))
    )
    return reply["id"]


def insert_many(handle: int, collection: str, documents: list[dict[str, Any]]) -> list[str]:
    reply = _call(
        _lib.nsq_insert_many,
        _c_ll(handle),
        _utf8(collection),
        _utf8(json.dumps(documents)),
    )
    return reply["ids"]


def find(handle: int, collection: str, query: dict[str, Any]) -> list[dict[str, Any]]:
    reply = _call(
        _lib.nsq_find, _c_ll(handle), _utf8(collection), _utf8(json.dumps(query))
    )
    return reply["docs"]


def count(handle: int, collection: str, filter: dict[str, Any] | None) -> int:
    reply = _call(
        _lib.nsq_count, _c_ll(handle), _utf8(collection), _utf8(json.dumps(filter or {}))
    )
    return int(reply["count"])


def collections(handle: int) -> list[str]:
    return _call(_lib.nsq_collections, _c_ll(handle))["names"]
