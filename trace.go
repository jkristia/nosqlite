package nosqlite

// trace.go writes a plain-text record of everything the database does, next to
// the database file as <dbpath>.trace.
//
// This exists for exactly one purpose: being able to see what the database
// actually did. It is deliberately not a binary format, not a rotation scheme,
// and not a metrics system. The database never reads it back.
//
// Turn it on either in code:
//
//	nosqlite.Open("./demo.nsq", nosqlite.WithTrace(nosqlite.TraceAll))
//
// or, more usefully, without recompiling anything:
//
//	NOSQLITE_TRACE=all ./myprogram
//
// Format — one line per operation, aligned columns, greppable:
//
//	2026-08-09T14:23:01.412Z  000121  OPEN    -       path=./demo.nsq docs=1000000 colls=2   dur=884ms  ok
//	2026-08-09T14:23:01.455Z  000122  INSERT  users   _id=018f2c…001 off=309200144 len=299   dur=41µs   ok
//	2026-08-09T14:23:01.455Z  000123  SYNC    -                                              dur=1.9ms  ok
//	2026-08-09T14:23:02.101Z  000124  FIND    users   filter={"age":{"$gte":30}} sort=age:desc limit=10 scanned=1000000 matched=41871 returned=10  dur=1.284s  ok
//	2026-08-09T14:23:02.140Z  000125  INSERT  orders  _id=ord-1                              dur=12µs   ERROR duplicate _id "ord-1"
//
// Columns: timestamp, sequence number, operation, collection, details,
// duration, outcome. Three details earn their keep:
//
//   - scanned= / matched= / returned= on every query. With no indexes, the
//     ratio between those three numbers is the whole answer to "why is this
//     slow".
//   - off= and len= on every write: the exact byte range of the record in the
//     database file, so a trace line can be taken straight to `nsq dump` or a
//     hex editor.
//   - a monotonic sequence number, so operations from several threads can be
//     put back in order — wall-clock timestamps cannot do that at microsecond
//     spacing.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// tracer writes trace lines. Rules it must obey, in priority order:
//
//  1. A trace failure never fails an operation. Write errors are counted and
//     reported once on Close; the database carries on. Tracing is diagnostics,
//     not durability.
//  2. Line-buffered, never fsynced. Each line is one Write syscall, so the
//     lines leading up to a crash survive — but paying an fsync per line would
//     make the trace slower than the database.
//  3. Emitted after the operation completes, carrying its outcome and duration.
//  4. Size-capped, so it cannot fill the disk.
//  5. Its own mutex, independent of the data locks, so tracing a lock-free read
//     does not reintroduce a lock on the read path.
type tracer struct {
	mu        sync.Mutex
	file      *os.File
	level     TraceLevel
	seq       uint64
	written   int64
	maxBytes  int64
	truncated bool
	errCount  int
}

// newTracer opens the trace file. It never returns an error: if the trace file
// cannot be opened, tracing is silently disabled rather than failing the Open
// of a perfectly good database. A single complaint goes to stderr so the
// problem is not completely invisible.
func newTracer(path string, level TraceLevel, appendMode bool, maxBytes int64) *tracer {
	flags := os.O_WRONLY | os.O_CREATE
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nosqlite: cannot open trace file %s: %v (tracing disabled)\n", path, err)
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	return &tracer{file: f, level: level, maxBytes: maxBytes}
}

// enabled reports whether an operation at this level should be traced.
//
// The nil check is what makes every call site a one-liner: when tracing is off
// db.trace is nil, and calling a method on a nil pointer receiver is legal in
// Go as long as the method does not dereference it.
func (t *tracer) enabled(level TraceLevel) bool {
	return t != nil && t.level >= level
}

// next hands out the sequence number for an operation. Taken at the start of
// the operation so the numbers reflect start order.
func (t *tracer) next() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	return t.seq
}

// emit writes one line. op is e.g. "INSERT", coll is the collection name or "-".
func (t *tracer) emit(seq uint64, op, coll, details string, dur time.Duration, err error) {
	if t == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		// Keep the whole line on one line, whatever the error text contains.
		outcome = "ERROR " + strings.ReplaceAll(err.Error(), "\n", " ")
	}
	if coll == "" {
		coll = "-"
	}

	line := fmt.Sprintf("%s  %06d  %-7s %-10s %-60s dur=%-10s %s\n",
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		seq, op, coll, details, dur.String(), outcome)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.truncated {
		return
	}
	if t.written+int64(len(line)) > t.maxBytes {
		final := "TRACE TRUNCATED: reached the configured size cap\n"
		t.file.WriteString(final)
		t.truncated = true
		return
	}
	// One Write per line: the line reaches the OS immediately (rule 2), but we
	// never fsync it.
	n, werr := t.file.WriteString(line)
	t.written += int64(n)
	if werr != nil {
		t.errCount++ // rule 1: counted, never propagated
	}
}

// close reports any accumulated write failures and closes the file.
func (t *tracer) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.errCount > 0 {
		fmt.Fprintf(os.Stderr, "nosqlite: %d trace lines could not be written\n", t.errCount)
	}
	t.file.Close()
	t.file = nil
}

// ---------------------------------------------------------------------------
// Call sites — one small method per traced operation, so the callers in the
// rest of the package stay one line each.
// ---------------------------------------------------------------------------

// Go's `time.Since(started)` is the idiomatic stopwatch: it subtracts a stored
// time.Time from now and yields a time.Duration that prints as "41µs".

func (db *DB) traceOpen(started time.Time) {
	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	details := fmt.Sprintf("path=%s docs=%d colls=%d sync=%s", db.path, db.total, len(db.catalog), syncName(db.cfg.sync))
	db.trace.emit(seq, "OPEN", "-", details, time.Since(started), nil)
}

func (db *DB) traceClose(started time.Time, err error) {
	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	db.trace.emit(seq, "CLOSE", "-", "path="+db.path, time.Since(started), err)
}

func (db *DB) traceSync(started time.Time, err error) {
	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	db.trace.emit(seq, "SYNC", "-", "", time.Since(started), err)
}

// traceInsert reports one insert. off/len are the byte range of the whole
// framed record — header included — so the number is directly usable as
// `nsq dump --from <off>`.
func (db *DB) traceInsert(coll, id string, off int64, total int, payload []byte,
	started time.Time, err error) {

	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	details := fmt.Sprintf("_id=%s off=%d len=%d", id, off, total)
	if db.trace.enabled(TraceVerbose) && payload != nil {
		details += " doc=" + truncateForTrace(string(payload), 512)
	}
	db.trace.emit(seq, "INSERT", coll, details, time.Since(started), err)
}

func (db *DB) traceInsertMany(coll string, count int, off int64, total int,
	started time.Time, err error) {

	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	details := fmt.Sprintf("count=%d off=%d len=%d", count, off, total)
	db.trace.emit(seq, "INSERTM", coll, details, time.Since(started), err)
}

// traceFind reports a query together with the three numbers that explain its
// cost. Queries are TraceAll rather than TraceWrites, since a read-heavy
// workload would otherwise flood the file.
func (db *DB) traceFind(coll string, q Query, stats scanStats, dur time.Duration, err error) {
	if !db.trace.enabled(TraceAll) {
		return
	}
	seq := db.trace.next()
	details := fmt.Sprintf("%s scanned=%d matched=%d returned=%d",
		q.String(), stats.scanned, stats.matched, stats.returned)
	db.trace.emit(seq, "FIND", coll, details, dur, err)
}

func (db *DB) traceDefine(coll string, id uint16) {
	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	db.trace.emit(seq, "DEFINE", coll, fmt.Sprintf("id=%d", id), 0, nil)
}

// traceRecovered reports a repaired torn tail. Always worth a line: it means
// the process died mid-write last time.
func (db *DB) traceRecovered(off int64, reason string) {
	if !db.trace.enabled(TraceWrites) {
		return
	}
	seq := db.trace.next()
	db.trace.emit(seq, "RECOVER", "-", fmt.Sprintf("truncated_to=%d reason=%q", off, reason), 0, nil)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func syncName(m SyncMode) string {
	if m == SyncNever {
		return "never"
	}
	return "always"
}

// truncateForTrace keeps a long document from taking over the trace file.
func truncateForTrace(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("…(+%d bytes)", len(s)-max)
}

// compactJSON renders a value as one line of JSON for trace output. Used by
// Query.String, so it must never fail loudly — a broken trace line is better
// than a broken query.
func compactJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
