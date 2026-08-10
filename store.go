package nosqlite

// store.go is the storage engine: the on-disk layout, the append path, replay
// at Open, and torn-tail recovery.
//
// The file is an append-only log. Nothing is ever overwritten in place, which
// is what makes crash recovery a two-line rule and lets readers run without a
// lock (see index.go).
//
// Layout
// ------
//
//	byte 0                     32                              EOF
//	  +---------------------+  +--------+--------+--------+ ...
//	  |    file header      |  | record | record | record |
//	  |      32 bytes       |  +--------+--------+--------+
//	  +---------------------+
//
// File header (32 bytes, written once at creation):
//
//	magic "NSQLITE\n"  8 bytes
//	format   uint16    2 bytes   (little-endian)
//	flags    uint16    2 bytes
//	created  uint64    8 bytes   (Unix nanoseconds)
//	reserved          12 bytes   (must be zero)
//
// Record header (12 bytes) followed by exactly `length` payload bytes:
//
//	length   uint32    4 bytes   payload length
//	op       uint8     1 byte    what kind of record this is
//	flags    uint8     1 byte    zero in v1
//	coll     uint16    2 bytes   which collection the record belongs to
//	crc32    uint32    4 bytes   IEEE CRC over op‖flags‖coll‖payload

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"time"
)

const (
	// magic is the first 8 bytes of every database file. Opening a JPEG as a
	// database then produces "not a nosqlite file" instead of a baffling parse
	// error further in.
	magic = "NSQLITE\n"

	headerSize       = 32 // file header
	recordHeaderSize = 12 // per-record header

	// formatVersion is bumped only when the on-disk layout changes in a way an
	// older binary cannot read. Open refuses anything it does not recognise.
	formatVersion uint16 = 1

	// maxPayloadSize is a sanity bound. A length field larger than this is
	// treated as garbage rather than as a reason to allocate 3 GB.
	maxPayloadSize = 64 << 20 // 64 MB
)

// Record operation codes. Values 2, 3, 5 and 6 are *reserved*: v1 never writes
// them, and reading one is a hard error rather than a skip, so an old binary
// fails loudly against a file written by a newer one instead of silently
// ignoring its deletes.
const (
	opInsert           uint8 = 1 // payload is the document JSON
	opDelete           uint8 = 2 // reserved: payload would be the _id
	opReplace          uint8 = 3 // reserved: payload would be the new document
	opDefineCollection uint8 = 4 // payload is {"id":7,"name":"users"}
	opBegin            uint8 = 5 // reserved: multi-document atomicity
	opCommit           uint8 = 6 // reserved: multi-document atomicity
)

// opName renders an op code for error messages, traces and the nsq CLI.
func opName(op uint8) string {
	switch op {
	case opInsert:
		return "insert"
	case opDelete:
		return "delete"
	case opReplace:
		return "replace"
	case opDefineCollection:
		return "define"
	case opBegin:
		return "begin"
	case opCommit:
		return "commit"
	default:
		return fmt.Sprintf("op(%d)", op)
	}
}

// crcTable is the polynomial table for IEEE CRC32. Hoisted into a package-level
// variable so the hot path does not look it up every record; on most CPUs
// Go uses a hardware CRC instruction anyway.
var crcTable = crc32.IEEETable

// recordCRC computes the checksum the way the format defines it: over the
// op/flags/coll bytes of the header, then the payload.
func recordCRC(meta []byte, payload []byte) uint32 {
	c := crc32.Update(0, crcTable, meta)
	return crc32.Update(c, crcTable, payload)
}

// ---------------------------------------------------------------------------
// File header
// ---------------------------------------------------------------------------

// writeFileHeader lays down the 32-byte header of a brand-new database.
// Caller must hold db.mu for writing (or be inside Open, where nobody else has
// a reference to db yet).
func (db *DB) writeFileHeader() error {
	var hdr [headerSize]byte
	copy(hdr[0:8], magic)
	binary.LittleEndian.PutUint16(hdr[8:10], formatVersion)
	binary.LittleEndian.PutUint16(hdr[10:12], 0) // flags
	binary.LittleEndian.PutUint64(hdr[12:20], uint64(time.Now().UnixNano()))
	// hdr[20:32] stays zero: reserved, and validated as zero on read so it can
	// be claimed later without ambiguity.

	if _, err := db.file.WriteAt(hdr[:], 0); err != nil {
		return fmt.Errorf("nosqlite: writing file header: %w", err)
	}
	if err := db.file.Sync(); err != nil {
		return fmt.Errorf("nosqlite: syncing file header: %w", err)
	}
	db.size = headerSize
	return nil
}

// readFileHeader validates the header of an existing file.
func (db *DB) readFileHeader(fileSize int64) error {
	if fileSize < headerSize {
		// Too short to even be a header. This is not a torn record — records
		// start after the header — so it is not recoverable.
		return fmt.Errorf("%w: %s is only %d bytes", ErrNotDatabase, db.path, fileSize)
	}
	var hdr [headerSize]byte
	if _, err := db.file.ReadAt(hdr[:], 0); err != nil {
		return fmt.Errorf("nosqlite: reading file header: %w", err)
	}
	if string(hdr[0:8]) != magic {
		return fmt.Errorf("%w: %s", ErrNotDatabase, db.path)
	}
	if got := binary.LittleEndian.Uint16(hdr[8:10]); got != formatVersion {
		return fmt.Errorf("nosqlite: %s is format version %d, this build understands %d",
			db.path, got, formatVersion)
	}
	if got := binary.LittleEndian.Uint16(hdr[10:12]); got != 0 {
		return fmt.Errorf("nosqlite: %s has unknown header flags %#x", db.path, got)
	}
	for _, b := range hdr[20:32] {
		if b != 0 {
			return fmt.Errorf("nosqlite: %s has non-zero reserved header bytes", db.path)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Appending
// ---------------------------------------------------------------------------

// encodeRecord writes one framed record into dst, which must be exactly
// recordHeaderSize+len(payload) bytes long.
func encodeRecord(dst []byte, op uint8, coll uint16, payload []byte) {
	binary.LittleEndian.PutUint32(dst[0:4], uint32(len(payload)))
	dst[4] = op
	dst[5] = 0 // flags: zero in v1
	binary.LittleEndian.PutUint16(dst[6:8], coll)
	copy(dst[recordHeaderSize:], payload)
	binary.LittleEndian.PutUint32(dst[8:12], recordCRC(dst[4:8], payload))
}

// appendRecord appends one record and returns the offset the record starts at
// and its total on-disk length (header + payload).
//
// Caller must hold db.mu for writing. It does NOT sync — the caller decides,
// because InsertMany wants one sync for a whole batch.
func (db *DB) appendRecord(op uint8, coll uint16, payload []byte) (off int64, total int, err error) {
	if len(payload) > maxPayloadSize {
		return 0, 0, fmt.Errorf("nosqlite: record of %d bytes exceeds the %d byte limit",
			len(payload), maxPayloadSize)
	}
	total = recordHeaderSize + len(payload)

	// Reuse db.wbuf across calls instead of allocating per insert.
	// `buf[:n]` reslices; the make() only happens when the buffer is too small.
	if cap(db.wbuf) < total {
		db.wbuf = make([]byte, total)
	}
	buf := db.wbuf[:total]
	encodeRecord(buf, op, coll, payload)

	off = db.size
	// WriteAt writes at an explicit offset without touching the file's shared
	// seek position — which matters because concurrent readers use ReadAt on
	// the very same *os.File.
	n, err := db.file.WriteAt(buf, off)
	if err != nil {
		// A partial write leaves a torn record at the tail. That is fine: we do
		// not advance db.size, so the next append overwrites it, and if we
		// crash right now the truncate-on-open rule cleans it up.
		return 0, 0, fmt.Errorf("nosqlite: appending record at %d (wrote %d of %d bytes): %w",
			off, n, total, err)
	}
	db.size += int64(total)
	db.needSync = true
	return off, total, nil
}

// appendBatch appends several records with a single write syscall. It returns
// the number of records that made it to disk in full.
//
// This is deliberately NOT atomic: on a partial write a prefix of the batch
// lands and the rest does not. Callers report how many were written.
func (db *DB) appendBatch(op uint8, coll uint16, payloads [][]byte) (offs []int64, written int, err error) {
	total := 0
	for _, p := range payloads {
		if len(p) > maxPayloadSize {
			return nil, 0, fmt.Errorf("nosqlite: record of %d bytes exceeds the %d byte limit",
				len(p), maxPayloadSize)
		}
		total += recordHeaderSize + len(p)
	}
	if cap(db.wbuf) < total {
		db.wbuf = make([]byte, total)
	}
	buf := db.wbuf[:total]

	offs = make([]int64, len(payloads))
	pos := 0
	for i, p := range payloads {
		offs[i] = db.size + int64(pos)
		size := recordHeaderSize + len(p)
		encodeRecord(buf[pos:pos+size], op, coll, p)
		pos += size
	}

	batchStart := db.size
	n, writeErr := db.file.WriteAt(buf, batchStart)

	// Work out how many complete records the write actually covered.
	written = 0
	acc := 0
	for _, p := range payloads {
		acc += recordHeaderSize + len(p)
		if acc <= n {
			written++
		} else {
			break
		}
	}
	// Only advance the append point past the records that fully landed, so a
	// torn record at the end gets overwritten by the next append.
	consumed := 0
	for i := 0; i < written; i++ {
		consumed += recordHeaderSize + len(payloads[i])
	}
	db.size += int64(consumed)
	if consumed > 0 {
		db.needSync = true
	}

	if writeErr != nil {
		return offs[:written], written, fmt.Errorf(
			"nosqlite: appending batch at %d (wrote %d of %d bytes, %d of %d records): %w",
			batchStart, n, total, written, len(payloads), writeErr)
	}
	return offs, written, nil
}

// syncIfNeeded applies the configured durability policy after a write.
// Caller must hold db.mu for writing.
func (db *DB) syncIfNeeded() error {
	if db.cfg.sync != SyncAlways {
		return nil
	}
	started := time.Now()
	err := db.file.Sync()
	if err == nil {
		db.needSync = false
	}
	db.traceSync(started, err)
	return err
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// replay walks every record in the file and rebuilds the in-memory index.
//
// It does not unmarshal documents: for an insert it only records where the
// payload lives and how long it is. The only records parsed as JSON are the
// rare "define collection" ones.
//
// Torn trailing record: a crash mid-append can leave a partial record at the
// end of the file. Because appends are the only writes, damage can only ever be
// at the tail, so recovery is unambiguous — truncate back to the end of the
// last good record. The in-flight insert is lost, which is correct: it never
// returned success to the caller.
func (db *DB) replay(fileSize int64) error {
	// A SectionReader is a view of part of a file (here: everything after the
	// file header). Wrapping it in a bufio.Reader turns a million small reads
	// into a handful of big ones.
	section := io.NewSectionReader(db.file, headerSize, fileSize-headerSize)
	r := bufio.NewReaderSize(section, 64<<10)

	var hdr [recordHeaderSize]byte
	var payload []byte // scratch, grown as needed and reused

	off := int64(headerSize) // offset of the record we are about to read

	for {
		// io.ReadFull returns:
		//   io.EOF              nothing left at all -> clean end of file
		//   io.ErrUnexpectedEOF some bytes but not all -> torn tail
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF {
				return db.truncateTail(off, "partial record header")
			}
			return fmt.Errorf("nosqlite: reading record header at %d: %w", off, err)
		}

		length := binary.LittleEndian.Uint32(hdr[0:4])
		op := hdr[4]
		flags := hdr[5]
		coll := binary.LittleEndian.Uint16(hdr[6:8])
		wantCRC := binary.LittleEndian.Uint32(hdr[8:12])

		recEnd := off + recordHeaderSize + int64(length)
		isLast := recEnd >= fileSize

		if length > maxPayloadSize || recEnd > fileSize {
			// The header promises more payload than the file contains, or an
			// absurd length. Either way this is the torn tail.
			//
			// Known limitation: the checksum covers op/flags/coll and the
			// payload, but NOT the length field (that is what the format
			// specifies). So bit rot in a length field mid-file is
			// indistinguishable from a torn tail, and lands here — truncating
			// the file rather than reporting corruption. Widening the CRC to
			// cover the length would close the gap, at the cost of a format
			// version bump.
			return db.truncateTail(off, "record length exceeds file")
		}
		if flags != 0 {
			return fmt.Errorf("nosqlite: record at %d has unknown flags %#x", off, flags)
		}

		// Read (or skip) the payload.
		//
		// Define-collection records are always read: we need their contents.
		// Under WithFastOpen everything else is skipped without a CRC check,
		// except the final record, which still gets checked so torn-tail
		// recovery keeps working.
		mustRead := !db.cfg.fastOpen || op == opDefineCollection || isLast
		if mustRead {
			if cap(payload) < int(length) {
				payload = make([]byte, length)
			}
			payload = payload[:length]
			if _, err := io.ReadFull(r, payload); err != nil {
				return db.truncateTail(off, "payload shorter than declared")
			}
			if got := recordCRC(hdr[4:8], payload); got != wantCRC {
				if isLast {
					// A bad checksum on the last record means the write did not
					// complete. Repair by truncating.
					return db.truncateTail(off, "checksum mismatch on final record")
				}
				// A bad checksum in the middle is real corruption (bit rot).
				// Refuse to open rather than guess.
				return fmt.Errorf("%w: checksum mismatch at offset %d", ErrCorrupt, off)
			}
		} else {
			// Discard skips bytes without copying them out of the buffer.
			if _, err := r.Discard(int(length)); err != nil {
				return db.truncateTail(off, "payload shorter than declared")
			}
			payload = payload[:0]
		}

		// Dispatch on the record type.
		switch op {
		case opDefineCollection:
			if err := db.applyDefineCollection(payload); err != nil {
				return fmt.Errorf("nosqlite: bad collection record at %d: %w", off, err)
			}
		case opInsert:
			c, ok := db.byID[coll]
			if !ok {
				return fmt.Errorf("%w: insert at %d names undefined collection id %d",
					ErrCorrupt, off, coll)
			}
			// The index stores the offset of the *payload*, not of the record
			// header, so a read is one ReadAt with no header parsing.
			c.appendIndex(off+recordHeaderSize, length)
			db.total++
		case opDelete, opReplace, opBegin, opCommit:
			return fmt.Errorf("nosqlite: record at %d uses %s, which this version does not support "+
				"(file written by a newer nosqlite?)", off, opName(op))
		default:
			return fmt.Errorf("nosqlite: record at %d has unknown op %d", off, op)
		}

		off = recEnd
	}

	db.size = off
	return nil
}

// truncateTail repairs a torn trailing record by cutting the file back to
// `off`, the end of the last good record. It always returns nil on success —
// a torn tail is expected after a crash, not an error.
func (db *DB) truncateTail(off int64, reason string) error {
	if err := db.file.Truncate(off); err != nil {
		return fmt.Errorf("nosqlite: truncating torn tail at %d: %w", off, err)
	}
	if err := db.file.Sync(); err != nil {
		return fmt.Errorf("nosqlite: syncing after truncate: %w", err)
	}
	db.size = off
	db.traceRecovered(off, reason)
	return nil
}

// ---------------------------------------------------------------------------
// Inspection — used by cmd/nsq, and useful in tests
// ---------------------------------------------------------------------------

// FileHeader is the decoded 32-byte file header.
type FileHeader struct {
	Format  uint16
	Flags   uint16
	Created time.Time
}

// RawRecord is one framed record as handed to the WalkFile callback.
//
// Payload points into a buffer that WalkFile reuses, so copy it if you need it
// after the callback returns.
type RawRecord struct {
	Offset  int64  // where the record header starts
	Length  uint32 // payload length
	Op      uint8
	Flags   uint8
	Coll    uint16
	Payload []byte
	CRCOK   bool // false means the payload does not match its checksum
}

// Total returns the full on-disk size of the record, header included.
func (r RawRecord) Total() int64 { return recordHeaderSize + int64(r.Length) }

// WalkFile opens a database file read-only and calls fn for every record in
// file order. Unlike Open it never truncates, never rebuilds an index, and
// keeps going past a checksum failure (reporting it via RawRecord.CRCOK) so a
// verify tool can find every bad record rather than only the first.
//
// Returning a non-nil error from fn stops the walk and returns that error.
func WalkFile(path string, fn func(RawRecord) error) (FileHeader, error) {
	var fh FileHeader

	f, err := os.Open(path) // read-only
	if err != nil {
		return fh, fmt.Errorf("nosqlite: opening %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fh, err
	}
	size := info.Size()
	if size < headerSize {
		return fh, fmt.Errorf("%w: %s is only %d bytes", ErrNotDatabase, path, size)
	}

	var hdr [headerSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return fh, err
	}
	if string(hdr[0:8]) != magic {
		return fh, fmt.Errorf("%w: %s", ErrNotDatabase, path)
	}
	fh.Format = binary.LittleEndian.Uint16(hdr[8:10])
	fh.Flags = binary.LittleEndian.Uint16(hdr[10:12])
	fh.Created = time.Unix(0, int64(binary.LittleEndian.Uint64(hdr[12:20])))

	r := bufio.NewReaderSize(f, 64<<10)
	var rh [recordHeaderSize]byte
	var payload []byte
	off := int64(headerSize)

	for {
		if _, err := io.ReadFull(r, rh[:]); err != nil {
			if err == io.EOF {
				return fh, nil
			}
			if err == io.ErrUnexpectedEOF {
				return fh, fmt.Errorf("nosqlite: torn record header at offset %d", off)
			}
			return fh, err
		}
		rec := RawRecord{
			Offset: off,
			Length: binary.LittleEndian.Uint32(rh[0:4]),
			Op:     rh[4],
			Flags:  rh[5],
			Coll:   binary.LittleEndian.Uint16(rh[6:8]),
		}
		wantCRC := binary.LittleEndian.Uint32(rh[8:12])

		if int64(rec.Length) > maxPayloadSize || off+rec.Total() > size {
			return fh, fmt.Errorf("nosqlite: torn record at offset %d (declares %d payload bytes, %d remain)",
				off, rec.Length, size-off-recordHeaderSize)
		}
		if cap(payload) < int(rec.Length) {
			payload = make([]byte, rec.Length)
		}
		payload = payload[:rec.Length]
		if _, err := io.ReadFull(r, payload); err != nil {
			return fh, fmt.Errorf("nosqlite: torn payload at offset %d", off)
		}
		rec.Payload = payload
		rec.CRCOK = recordCRC(rh[4:8], payload) == wantCRC

		if err := fn(rec); err != nil {
			return fh, err
		}
		off += rec.Total()
	}
}

// collectionDefinition is the payload of an opDefineCollection record.
// The `json:"..."` struct tags tell encoding/json which JSON key maps to which
// field — Go's equivalent of a serialisation annotation.
type collectionDefinition struct {
	ID   uint16 `json:"id"`
	Name string `json:"name"`
}

// DecodeCollectionRecord decodes a define-collection payload. Exported so the
// nsq CLI can label records with collection names without importing internals.
func DecodeCollectionRecord(payload []byte) (id uint16, name string, err error) {
	var def collectionDefinition
	if err := json.Unmarshal(payload, &def); err != nil {
		return 0, "", err
	}
	return def.ID, def.Name, nil
}
