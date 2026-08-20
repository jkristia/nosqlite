"""nosqlite -- an embedded document store, in one file, with a Mongo-ish API.

It should feel like pymongo if you have used pymongo, and like sqlite3 if you
have not::

    from nosqlite import Database

    with Database("./demo.nsq", trace="all") as db:
        users = db["users"]

        users.insert({"name": "Ada", "age": 36, "tags": ["math"]})
        users.insert_many([
            {"name": "Grace", "age": 45},
            {"name": "Alan",  "age": 41},
        ])

        for u in users.find({"age": {"$gte": 40}}, sort=[("age", -1)], limit=10):
            print(u["name"], u["age"])

        print(users.find_one({"name": "Ada"}))    # dict, or None
        print(users.count({"age": {"$gte": 40}})) # 2
        print(db.collections())                   # ['users']

The database file is the path you pass in. A trace file appears beside it as
``<path>.trace`` when tracing is on -- either via ``trace=`` above, or by
setting ``NOSQLITE_TRACE=all`` in the environment, which needs no code change
at all.
"""

from __future__ import annotations

from typing import Any, Iterator, Sequence

from . import _lib
from ._lib import NoSQLiteError

__all__ = ["Database", "Collection", "NoSQLiteError", "DEFAULT_LIMIT"]

#: ``find()`` applies this limit when the caller gives none.
#:
#: Without it, one absent-minded ``find({})`` over a large collection
#: materialises the whole thing three times over -- as a Go slice, as one giant
#: C string, and as a Python list. Surprising someone with a truncated list is
#: bad; surprising them with 4 GB of RSS is worse, so ``find`` raises rather
#: than truncating silently. Pass ``limit=0`` to genuinely ask for everything.
DEFAULT_LIMIT = 1000


class Database:
    """An open database file. Use it as a context manager."""

    def __init__(
        self,
        path: str,
        *,
        sync: str = "always",
        trace: str | None = None,
        trace_file: str | None = None,
        fast_open: bool = False,
    ) -> None:
        """Open (or create) the database at *path*.

        :param sync: ``"always"`` (default, durable) or ``"never"`` (fast bulk
            loading; flushed on close).
        :param trace: ``"writes"``, ``"all"`` or ``"verbose"`` to enable the
            trace file. The ``NOSQLITE_TRACE`` environment variable overrides
            this, and works without touching the code.
        :param trace_file: where to write the trace; default ``<path>.trace``.
        :param fast_open: skip checksum verification while replaying the file.
        """
        options: dict[str, Any] = {"sync": sync}
        if trace is not None:
            options["trace"] = trace
        if trace_file is not None:
            options["trace_file"] = trace_file
        if fast_open:
            options["fast_open"] = True

        self.path = path
        self._handle: int | None = _lib.open_database(path, options)

    # -- lifecycle ----------------------------------------------------------

    def close(self) -> None:
        """Close the database. Safe to call more than once."""
        if self._handle is not None:
            handle, self._handle = self._handle, None
            _lib.close_database(handle)

    def __enter__(self) -> "Database":
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def __del__(self) -> None:
        # A safety net for code that forgot the context manager. __del__ runs
        # during interpreter shutdown too, when imported modules may already be
        # torn down, so it must never raise.
        try:
            self.close()
        except Exception:
            pass

    @property
    def handle(self) -> int:
        if self._handle is None:
            raise NoSQLiteError("database is closed")
        return self._handle

    # -- collections --------------------------------------------------------

    def collection(self, name: str) -> "Collection":
        """Return a collection, creating it if it does not exist."""
        return Collection(self, name)

    def __getitem__(self, name: str) -> "Collection":
        """``db["users"]`` is shorthand for ``db.collection("users")``."""
        return self.collection(name)

    def collections(self) -> list[str]:
        """Names of every collection, sorted."""
        return _lib.collections(self.handle)

    def __repr__(self) -> str:
        state = "closed" if self._handle is None else "open"
        return f"<Database {self.path!r} ({state})>"


class Collection:
    """A named group of documents inside a :class:`Database`."""

    def __init__(self, database: Database, name: str) -> None:
        self.database = database
        self.name = name

    # -- writing ------------------------------------------------------------

    def insert(self, document: dict[str, Any]) -> str:
        """Store one document and return its ``_id``.

        If the document has no ``_id``, one is generated. Your dict is not
        modified -- assign the return value if you want the id in it.
        """
        return _lib.insert(self.database.handle, self.name, document)

    def insert_many(self, documents: Sequence[dict[str, Any]]) -> list[str]:
        """Store several documents with a single write and a single flush.

        Not atomic: on a crash a prefix of the batch can survive. The returned
        list holds the ids that were actually written.
        """
        return _lib.insert_many(self.database.handle, self.name, list(documents))

    def replace(
        self, filter: dict[str, Any] | None, document: dict[str, Any]
    ) -> int:
        """Overwrite the first document matching *filter*, and return how many
        were replaced -- ``1``, or ``0`` when nothing matched.

        *document* replaces the stored one entirely: fields it omits are gone,
        this is not a merge. At most one document is touched, so a too-broad
        filter cannot rewrite the collection.

        ``_id`` is immutable. The replacement keeps the matched document's id,
        and you may include the same ``_id`` in *document* but not a different
        one -- that raises :class:`NoSQLiteError`.

        That check is worth reaching for. An ``_id`` in *document* is not a
        second way to pick the document -- *filter* always picks -- it is an
        assertion that *filter* found the one you meant, and it costs nothing::

            # Meant document 7, but this filter matches document 8.
            users.replace({"name": "Emma Osei"}, {"_id": "7", "age": 33})
            # NoSQLiteError: nosqlite: _id is immutable: the filter matched
            # document "8", but the replacement carries _id "7"; one of them
            # is wrong

        Leave the ``_id`` out and that same typo overwrites document 8 silently.
        """
        return _lib.replace(self.database.handle, self.name, filter, document)

    def delete(self, filter: dict[str, Any]) -> int:
        """Remove the first document matching *filter*, and return how many were
        deleted -- ``1``, or ``0`` when nothing matched.

        "First" means insertion order, the same order :meth:`find_one` uses.
        Removing more than one at a time is :meth:`delete_many`, and the split
        is deliberate: a filter that matches more than you meant is the one
        mistake in this API with no undo, so the wider operation is the one you
        have to type more to get.

        *filter* is required rather than defaulting to "everything" for the same
        reason -- unlike :meth:`count` and :meth:`find`, an omitted filter here
        would empty the collection.

        A delete makes the file **bigger**, not smaller: it appends a tombstone
        and leaves the document where it was. Both are reclaimed only by a
        compaction.

        Deleting frees the ``_id``: inserting a new document under it afterwards
        is allowed and does not collide.
        """
        return _lib.delete(self.database.handle, self.name, filter)

    def delete_many(self, filter: dict[str, Any]) -> int:
        """Remove every document matching *filter*, and return how many were
        deleted.

        An empty filter matches everything and empties the collection. There is
        no confirmation step and no undo.

        Not atomic: the tombstones are written together, but a crash can leave a
        prefix of them on disk, so some documents are deleted and the rest
        survive. When that happens this raises :class:`NoSQLiteError`, and the
        count that actually landed is lost to the caller -- the C boundary
        reports it, but an exception carries no return value. Delete in smaller
        batches if you need to know exactly how far it got.
        """
        return _lib.delete_many(self.database.handle, self.name, filter)

    # -- reading ------------------------------------------------------------

    def find(
        self,
        filter: dict[str, Any] | None = None,
        *,
        projection: dict[str, Any] | None = None,
        sort: Sequence[tuple[str, int]] | None = None,
        skip: int = 0,
        limit: int | None = None,
    ) -> list[dict[str, Any]]:
        """Return the documents matching *filter*.

        :param filter: a Mongo-dialect filter, e.g. ``{"age": {"$gte": 30}}``.
            ``None`` matches everything.
        :param projection: which fields to return. ``{"name": 1,
            "address.city": 1}`` keeps only those; ``{"email": 0}`` keeps
            everything else. The two cannot be mixed in one projection --
            except ``_id``, which comes back unless you ask for ``{"_id": 0}``.
            A dotted key rebuilds the subdocument rather than flattening the
            key, so ``{"address.city": 1}`` yields
            ``{"address": {"city": "Oslo"}}``. ``None`` returns whole
            documents. The projection applies on the way out, so *filter* and
            *sort* may name fields it drops.
        :param sort: a list of ``(field, direction)`` pairs, where direction is
            ``1`` for ascending and ``-1`` for descending.
        :param skip: documents to drop before returning any.
        :param limit: maximum documents to return. ``None`` applies
            :data:`DEFAULT_LIMIT` and raises if that limit is actually hit;
            ``0`` means no limit at all.
        """
        explicit_limit = limit is not None
        effective = DEFAULT_LIMIT if limit is None else limit

        query = {
            "filter": filter or {},
            "projection": projection or {},
            "sort": _encode_sort(sort),
            "skip": skip,
            "limit": effective,
        }
        docs = _lib.find(self.database.handle, self.name, query)

        if not explicit_limit and len(docs) == DEFAULT_LIMIT:
            raise NoSQLiteError(
                f"find() hit the default limit of {DEFAULT_LIMIT} documents. "
                f"Pass limit=N for a bigger page, limit=0 for everything, or "
                f"use iter_find() to stream results without holding them all "
                f"in memory."
            )
        return docs

    def find_one(
        self,
        filter: dict[str, Any] | None = None,
        *,
        projection: dict[str, Any] | None = None,
    ) -> dict[str, Any] | None:
        """Return the first matching document, or ``None``.

        :param projection: which fields to return -- see :meth:`find`.
        """
        docs = self.find(filter, projection=projection, limit=1)
        return docs[0] if docs else None

    def iter_find(
        self,
        filter: dict[str, Any] | None = None,
        *,
        projection: dict[str, Any] | None = None,
        sort: Sequence[tuple[str, int]] | None = None,
        skip: int = 0,
        batch: int = 1000,
    ) -> Iterator[dict[str, Any]]:
        """Yield matching documents one at a time, paging under the hood.

        Python-side memory stays flat regardless of how many documents match,
        which is what you want over a large result set. Note that each page is
        a fresh query: with no sort the pages are stable because the underlying
        store is append-only, but a concurrent writer can add documents that a
        later page then includes.
        """
        if batch <= 0:
            raise ValueError("batch must be positive")
        offset = skip
        while True:
            page = self.find(filter, projection=projection, sort=sort, skip=offset, limit=batch)
            if not page:
                return
            yield from page
            if len(page) < batch:
                return
            offset += len(page)

    def count(self, filter: dict[str, Any] | None = None) -> int:
        """Count matching documents. An empty filter is answered instantly."""
        return _lib.count(self.database.handle, self.name, filter)

    def __len__(self) -> int:
        """``len(users)`` -- the number of documents in the collection."""
        return self.count()

    def __repr__(self) -> str:
        return f"<Collection {self.name!r}>"


def _encode_sort(sort: Sequence[tuple[str, int]] | None) -> list[dict[str, Any]]:
    """Turn ``[("age", -1)]`` into the wire format ``[{"field": ..., "desc": ...}]``."""
    if not sort:
        return []
    encoded: list[dict[str, Any]] = []
    for entry in sort:
        try:
            field, direction = entry
        except (TypeError, ValueError):
            raise ValueError(
                f"sort entries must be (field, direction) pairs, got {entry!r}"
            ) from None
        if direction not in (1, -1):
            raise ValueError(
                f"sort direction must be 1 (ascending) or -1 (descending), got {direction!r}"
            )
        encoded.append({"field": field, "desc": direction == -1})
    return encoded
