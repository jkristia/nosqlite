// Package nosqlite is a small embedded document store in the spirit of SQLite:
// no server, no daemon, one file on disk, linked directly into the host process.
//
// # Quick start
//
//	db, err := nosqlite.Open("./demo.nsq")
//	if err != nil { ... }
//	defer db.Close()
//
//	users, _ := db.Collection("users")
//	id, _ := users.Insert(map[string]any{"name": "Ada", "age": 36})
//
//	docs, _ := users.Find(nosqlite.Query{
//	    Filter: map[string]any{"age": map[string]any{"$gte": 30}},
//	    Sort:   []nosqlite.SortKey{{Field: "age", Desc: true}},
//	    Limit:  10,
//	})
//
// # v1 scope
//
// Exactly two operations: insert and query (filter / sort / skip / limit).
// No updates, deletes, projections or secondary indexes — see docs/design.md.
//
// # Numbers are float64
//
// Documents are decoded by encoding/json, and JSON has no integer type, so the
// number 42 comes back out of a query as float64(42), not int(42). Comparison
// and sorting treat all numbers as one type, so this only shows up when you
// type-assert a value in Go. From Python it is invisible.
//
// # Concurrency
//
// *DB and *Collection are safe for concurrent use by multiple goroutines. A
// query takes a point-in-time snapshot: documents inserted after the query
// started are not visible to it.
//
// # Multi-process
//
// Not supported. Two processes opening the same file will corrupt it; there is
// no file lock in v1.
package nosqlite

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
//
// In Go, a "sentinel error" is a package-level error value that callers can
// compare against with errors.Is(err, nosqlite.ErrClosed). It is the Go
// equivalent of defining a specific exception class in Python.
// ---------------------------------------------------------------------------

var (
	// ErrClosed is returned by any operation on a database that has been closed.
	ErrClosed = errors.New("nosqlite: database is closed")

	// ErrStop can be returned from a ForEach callback to halt the scan early
	// without ForEach itself reporting an error.
	ErrStop = errors.New("nosqlite: stop iteration")

	// ErrDuplicateID is returned when inserting a caller-supplied _id that
	// already exists in the collection.
	ErrDuplicateID = errors.New("nosqlite: duplicate _id")

	// ErrNotDatabase is returned by Open when the file exists but is not a
	// nosqlite database (bad magic bytes).
	ErrNotDatabase = errors.New("nosqlite: not a nosqlite file")

	// ErrCorrupt is returned when a record in the middle of the file fails its
	// CRC check. (A bad record at the very end of the file is a torn write and
	// is repaired automatically instead — see docs/design.md §3.)
	ErrCorrupt = errors.New("nosqlite: database file is corrupt")
)

// ---------------------------------------------------------------------------
// Options
//
// Go has no keyword or default arguments, so the idiomatic way to give a
// constructor optional settings is the "functional options" pattern: Open takes
// a variadic list of functions, each of which mutates a private config struct.
// Adding a new option later never changes Open's signature.
// ---------------------------------------------------------------------------

// SyncMode controls how often the database asks the operating system to flush
// writes all the way to the physical disk (fsync).
type SyncMode int

const (
	// SyncAlways fsyncs after every write. A completed Insert survives a
	// `kill -9` or a power cut. This is the default, and it costs one disk
	// flush per insert.
	SyncAlways SyncMode = iota

	// SyncNever fsyncs only on Close. Much faster for bulk loading, but a
	// crash may lose recent inserts. It never leaves half a document on disk:
	// atomicity and durability are separate properties.
	SyncNever
)

// TraceLevel controls how much detail goes into the human-readable trace file
// (see trace.go). The trace file is never read back by the database.
type TraceLevel int

const (
	TraceOff     TraceLevel = iota // default; the NOSQLITE_TRACE env var overrides it
	TraceWrites                    // inserts, opens, closes, syncs, errors
	TraceAll                       // + queries, with scanned/matched/returned counts
	TraceVerbose                   // + document payloads, truncated at 512 bytes
)

// String makes TraceLevel print as a word rather than a number. Implementing
// String() is how Go types customise their %v / fmt.Println formatting.
func (l TraceLevel) String() string {
	switch l {
	case TraceWrites:
		return "writes"
	case TraceAll:
		return "all"
	case TraceVerbose:
		return "verbose"
	default:
		return "off"
	}
}

// config holds everything the options can set. It is unexported (lower-case
// name), so callers can only reach it through the Option functions below.
type config struct {
	sync          SyncMode
	fastOpen      bool
	trace         TraceLevel
	traceFile     string
	traceAppend   bool
	traceMaxBytes int64
}

func defaultConfig() config {
	return config{
		sync:          SyncAlways,
		trace:         TraceOff,
		traceMaxBytes: 64 << 20, // 64 MB
	}
}

// Option is a function that customises Open. See WithSync, WithTrace, etc.
type Option func(*config)

// WithSync selects the durability policy. Default is SyncAlways.
func WithSync(mode SyncMode) Option { return func(c *config) { c.sync = mode } }

// WithFastOpen skips CRC verification while replaying the file at Open. Faster,
// but bit rot in the middle of the file goes unnoticed until something reads
// that record. The final record is still checked, so torn-tail recovery still
// works.
func WithFastOpen() Option { return func(c *config) { c.fastOpen = true } }

// WithTrace enables the trace file at the given level. Default is TraceOff.
func WithTrace(level TraceLevel) Option { return func(c *config) { c.trace = level } }

// WithTraceFile overrides the trace file path. Default is <dbpath>.trace.
func WithTraceFile(path string) Option { return func(c *config) { c.traceFile = path } }

// WithTraceAppend keeps trace history across runs instead of truncating the
// trace file at Open.
func WithTraceAppend() Option { return func(c *config) { c.traceAppend = true } }

// WithTraceMaxBytes caps the trace file size. On reaching the cap the tracer
// writes one final "TRACE TRUNCATED" line and stops. Default 64 MB.
func WithTraceMaxBytes(n int64) Option { return func(c *config) { c.traceMaxBytes = n } }

// traceLevelFromEnv reads NOSQLITE_TRACE. It returns ok=false when the variable
// is unset, so an unset variable leaves the code's own setting alone.
//
// When it *is* set it wins over WithTrace — the whole point is being able to
// turn tracing on for an already-compiled program at 2am.
func traceLevelFromEnv() (TraceLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NOSQLITE_TRACE"))) {
	case "":
		return TraceOff, false
	case "off", "0", "false":
		return TraceOff, true
	case "writes", "write":
		return TraceWrites, true
	case "all", "1", "true":
		return TraceAll, true
	case "verbose", "debug":
		return TraceVerbose, true
	default:
		// An unrecognised value is not worth failing an Open over.
		return TraceOff, false
	}
}

// ---------------------------------------------------------------------------
// DB
// ---------------------------------------------------------------------------

// DB is an open database: one file, holding every collection.
//
// The zero value is not usable; get one from Open.
type DB struct {
	// mu guards the single append point, the file size, the catalog, and the
	// per-collection index arrays.
	//
	// RWMutex allows either one writer or any number of concurrent readers.
	// Readers only hold it for the few nanoseconds it takes to copy a snapshot
	// (see index.go), never for the duration of a scan.
	mu sync.RWMutex

	file *os.File // the one database file, opened read/write
	path string   // as passed to Open
	size int64    // current file size == the offset of the next append
	cfg  config

	catalog map[string]*Collection // name  -> collection
	byID    map[uint16]*Collection // id    -> collection (used during replay)
	nextID  uint16                 // next collection id to hand out; ids start at 1
	total   int                    // total insert records in the whole file

	needSync bool // true when SyncNever has buffered writes not yet fsynced
	closed   bool

	trace *tracer // nil when tracing is off

	// wbuf is a scratch buffer reused for building records to append. It is
	// only ever touched while holding mu for writing, so it needs no lock of
	// its own. Reusing it avoids one heap allocation per insert.
	wbuf []byte
}

// Open opens (or creates) the database file at path.
//
// path is the database FILE, not a directory: Open("./demo.nsq") gives you
// ./demo.nsq, and optionally ./demo.nsq.trace beside it.
//
// Opening replays the whole file to rebuild the in-memory index, which costs
// one sequential read. Nothing is parsed as JSON during replay except the tiny
// "define collection" records, so this is fast — roughly a second for a 300 MB
// database.
func Open(path string, opts ...Option) (*DB, error) {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	// The environment variable overrides the compiled-in setting.
	if lvl, ok := traceLevelFromEnv(); ok {
		cfg.trace = lvl
	}

	started := time.Now()

	// O_RDWR|O_CREATE: open for reading and writing, create if missing.
	// 0o644 is the Unix permission for "owner writes, everyone reads".
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("nosqlite: opening %s: %w", path, err)
	}

	db := &DB{
		file:    f,
		path:    path,
		cfg:     cfg,
		catalog: make(map[string]*Collection),
		byID:    make(map[uint16]*Collection),
		nextID:  1,
	}

	// Start the tracer before doing any work, so a failure during replay is
	// itself traced.
	if cfg.trace > TraceOff {
		tracePath := cfg.traceFile
		if tracePath == "" {
			tracePath = path + ".trace"
		}
		db.trace = newTracer(tracePath, cfg.trace, cfg.traceAppend, cfg.traceMaxBytes)
	}

	// Stat tells us whether this is a brand-new (zero-length) file.
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("nosqlite: stat %s: %w", path, err)
	}

	if info.Size() == 0 {
		// Brand new database: lay down the 32-byte file header.
		if err := db.writeFileHeader(); err != nil {
			f.Close()
			return nil, err
		}
	} else {
		// Existing database: validate the header, then replay the records.
		if err := db.readFileHeader(info.Size()); err != nil {
			f.Close()
			return nil, err
		}
		if err := db.replay(info.Size()); err != nil {
			f.Close()
			return nil, err
		}
	}

	db.traceOpen(started)
	return db, nil
}

// Path returns the database file path this DB was opened with.
func (db *DB) Path() string { return db.path }

// Close flushes anything outstanding and closes the file. It is safe to call
// more than once; later calls are no-ops.
//
// Idiomatic Go calls this with `defer db.Close()` immediately after Open.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	started := time.Now()
	var firstErr error

	// Under SyncNever we owe the caller one final flush.
	if db.needSync {
		if err := db.file.Sync(); err != nil {
			firstErr = fmt.Errorf("nosqlite: final sync: %w", err)
		}
		db.needSync = false
	}
	if err := db.file.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("nosqlite: closing %s: %w", db.path, err)
	}

	db.traceClose(started, firstErr)
	if db.trace != nil {
		db.trace.close()
		db.trace = nil
	}
	return firstErr
}

// Collection returns the named collection, creating it if it does not exist.
//
// Creation appends a small "define collection" record to the file, so a
// collection with zero documents still exists after reopening the database.
//
// Names must match [A-Za-z0-9_-]{1,64}.
func (db *DB) Collection(name string) (*Collection, error) {
	// Fast path: it usually already exists, and a read lock lets concurrent
	// callers through in parallel.
	db.mu.RLock()
	c, ok := db.catalog[name]
	closed := db.closed
	db.mu.RUnlock()
	if ok {
		return c, nil
	}
	if closed {
		return nil, ErrClosed
	}
	if err := validateCollectionName(name); err != nil {
		return nil, err
	}

	// Slow path: take the write lock and create it.
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, ErrClosed
	}
	// Re-check under the write lock: another goroutine may have created it in
	// the window between releasing the read lock and taking the write lock.
	// This "double-checked" shape is standard for lazily created state.
	if c, ok := db.catalog[name]; ok {
		return c, nil
	}
	return db.defineCollection(name)
}

// Collections returns the names of all collections, sorted alphabetically.
// Answered from memory: no I/O.
func (db *DB) Collections() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	names := make([]string, 0, len(db.catalog))
	for name := range db.catalog {
		names = append(names, name)
	}
	// Go map iteration order is deliberately randomised, so anything that
	// wants a stable order has to sort explicitly.
	sort.Strings(names)
	return names
}

// Sync flushes buffered writes to disk. Only meaningful under SyncNever; under
// SyncAlways every write is already synced and this is a no-op.
func (db *DB) Sync() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if !db.needSync {
		return nil
	}
	started := time.Now()
	err := db.file.Sync()
	db.needSync = false
	db.traceSync(started, err)
	return err
}
