package nosqlite

// store_test.go covers the storage layer: framing, replay, and the crash
// recovery rules.
//
// The interesting cases are all about what the file looks like after a process
// dies mid-write, which the tests simulate by editing the file directly.

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFile creates a database with n documents in one collection and returns
// its path, with the database closed.
func buildFile(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recover.nsq")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	for i := 0; i < n; i++ {
		if _, err := c.Insert(map[string]any{"n": i, "pad": "some payload bytes"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// recordOffsets returns the start offset of every insert record in the file.
func recordOffsets(t *testing.T, path string) []int64 {
	t.Helper()
	var offs []int64
	_, err := WalkFile(path, func(r RawRecord) error {
		if r.Op == opInsert {
			offs = append(offs, r.Offset)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkFile: %v", err)
	}
	return offs
}

// appendBytes simulates a crash mid-append by leaving junk at the end of the
// file.
func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

// flipByte corrupts one byte, simulating bit rot.
func flipByte(t *testing.T, path string, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatal(err)
	}
}

func TestRecoversFromPartialRecordHeader(t *testing.T) {
	path := buildFile(t, 5)
	before, _ := os.Stat(path)

	// Fewer than 12 bytes of a record header: the write never got going.
	appendBytes(t, path, []byte{0x10, 0x00, 0x00})

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open should recover from a torn header: %v", err)
	}
	defer db.Close()

	if got := mustCollection(t, db, "c").Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
	after, _ := os.Stat(path)
	if after.Size() != before.Size() {
		t.Errorf("file is %d bytes after recovery, want it truncated back to %d",
			after.Size(), before.Size())
	}
}

func TestRecoversFromTruncatedPayload(t *testing.T) {
	path := buildFile(t, 5)
	before, _ := os.Stat(path)

	// A complete header promising 500 payload bytes, followed by only 4.
	hdr := make([]byte, recordHeaderSize+4)
	encodeRecord(hdr[:recordHeaderSize], opInsert, 1, nil)
	hdr[0] = 0xF4 // length = 500, little-endian
	hdr[1] = 0x01
	appendBytes(t, path, hdr)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open should recover from a truncated payload: %v", err)
	}
	defer db.Close()

	if got := mustCollection(t, db, "c").Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
	after, _ := os.Stat(path)
	if after.Size() != before.Size() {
		t.Errorf("file is %d bytes after recovery, want %d", after.Size(), before.Size())
	}
}

func TestRecoversFromBadChecksumOnLastRecord(t *testing.T) {
	path := buildFile(t, 5)
	offs := recordOffsets(t, path)
	last := offs[len(offs)-1]

	// Corrupt the payload of the final record. A checksum failure at EOF means
	// the write did not complete, so it is repaired rather than reported.
	flipByte(t, path, last+recordHeaderSize+2)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open should recover from a torn final record: %v", err)
	}
	defer db.Close()

	if got := mustCollection(t, db, "c").Len(); got != 4 {
		t.Errorf("Len() = %d, want 4 (the torn insert is correctly lost)", got)
	}
	info, _ := os.Stat(path)
	if info.Size() != last {
		t.Errorf("file is %d bytes, want it truncated to %d", info.Size(), last)
	}
}

func TestMidFileCorruptionRefusesToOpen(t *testing.T) {
	path := buildFile(t, 5)
	offs := recordOffsets(t, path)

	// Corrupt a record in the MIDDLE. That is real bit rot, not a torn write,
	// so Open must refuse rather than guess.
	flipByte(t, path, offs[1]+recordHeaderSize+2)

	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a file with a corrupt record in the middle")
	}

	// WithFastOpen skips checksums, so the same file opens — that is the
	// documented trade, and worth pinning down in a test.
	db, err := Open(path, WithFastOpen())
	if err != nil {
		t.Fatalf("WithFastOpen should not verify checksums: %v", err)
	}
	db.Close()
}

func TestWalkFileReportsBadChecksums(t *testing.T) {
	path := buildFile(t, 5)
	offs := recordOffsets(t, path)
	flipByte(t, path, offs[2]+recordHeaderSize+1)

	bad := 0
	records := 0
	_, err := WalkFile(path, func(r RawRecord) error {
		records++
		if !r.CRCOK {
			bad++
			if r.Offset != offs[2] {
				t.Errorf("bad record reported at %d, want %d", r.Offset, offs[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkFile: %v", err)
	}
	if records != 6 { // 5 inserts + 1 define-collection
		t.Errorf("walked %d records, want 6", records)
	}
	if bad != 1 {
		t.Errorf("found %d bad records, want 1", bad)
	}
}

func TestIndexOffsetsPointAtPayloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "offsets.nsq")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	for i := 0; i < 3; i++ {
		if _, err := c.Insert(map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}

	snap := c.snapshot()
	offs := recordOffsets(t, path)
	if len(offs) != snap.n() {
		t.Fatalf("file has %d insert records, index has %d entries", len(offs), snap.n())
	}
	for i := range offs {
		// The index stores where the PAYLOAD starts, one record header past the
		// record itself.
		if snap.offsets[i] != offs[i]+recordHeaderSize {
			t.Errorf("index offset %d = %d, want %d", i, snap.offsets[i], offs[i]+recordHeaderSize)
		}
	}
	db.Close()
}

func TestIDTableSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ids.nsq")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := mustCollection(t, db, "c")
	// A mix of generated and supplied ids, so the table has to be built over
	// documents that were inserted before it existed.
	if _, err := c.Insert(map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(map[string]any{"_id": "custom", "n": 2}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c2 := mustCollection(t, reopened, "c")

	// The table is rebuilt lazily on this insert, from the file.
	if _, err := c2.Insert(map[string]any{"_id": "custom", "n": 3}); err == nil {
		t.Error("duplicate _id should be rejected after reopening")
	}
	if _, err := c2.Insert(map[string]any{"_id": "other", "n": 3}); err != nil {
		t.Errorf("a fresh id should be accepted: %v", err)
	}
}

// TestIDTableGrowth pushes enough ids through the open-addressed table to force
// several resizes.
func TestIDTableGrowth(t *testing.T) {
	db := tempDB(t, WithSync(SyncNever))
	c := mustCollection(t, db, "c")

	const n = 2000
	for i := 0; i < n; i++ {
		id := "id-" + itoa(i)
		if _, err := c.Insert(map[string]any{"_id": id, "n": i}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if c.Len() != n {
		t.Fatalf("Len() = %d, want %d", c.Len(), n)
	}
	// Every one of them must now be rejected as a duplicate.
	for i := 0; i < n; i += 97 {
		if _, err := c.Insert(map[string]any{"_id": "id-" + itoa(i)}); err == nil {
			t.Fatalf("id-%d was not detected as a duplicate", i)
		}
	}
}

// itoa avoids importing strconv just for the tests above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestScanLiveCountsSupersededRecords exercises the offline live/dead analysis
// against a file containing replaces and deletes.
//
// Nothing writes op=2 or op=3 yet, and replay rejects both, so the test builds
// such a file the only way currently possible: by appending the records
// directly, below the API. That is deliberate — ScanLive has to be right before
// the write paths land, since it is how `nsq stat` will report whether
// compaction is worth running.
func TestScanLiveCountsSupersededRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.nsq")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c, err := db.Collection("users")
	if err != nil {
		t.Fatalf("Collection: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Insert(map[string]any{"_id": id, "v": 1}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	// Record the sizes of the two records about to be superseded, so the
	// expected dead-byte total is derived from the file rather than guessed.
	db.mu.Lock()
	deadA := recordHeaderSize + int64(c.lengths[0]) // "a", about to be replaced
	deadB := recordHeaderSize + int64(c.lengths[1]) // "b", about to be deleted

	// Replace "a" with a bigger document, then delete "b".
	replacement := []byte(`{"_id":"a","v":2,"padding":"xxxxxxxxxxxxxxxx"}`)
	if _, _, err := db.appendRecord(opReplace, c.id, replacement); err != nil {
		db.mu.Unlock()
		t.Fatalf("appendRecord replace: %v", err)
	}
	tombstone := []byte(`{"_id":"b"}`)
	_, tombstoneTotal, err := db.appendRecord(opDelete, c.id, tombstone)
	if err != nil {
		db.mu.Unlock()
		t.Fatalf("appendRecord delete: %v", err)
	}
	db.mu.Unlock()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	stats, err := ScanLive(path)
	if err != nil {
		t.Fatalf("ScanLive: %v", err)
	}

	// "a" survives as its replacement, "b" is gone, "c" is untouched.
	if got := stats.Documents[c.id]; got != 2 {
		t.Errorf("live documents = %d, want 2", got)
	}
	// The old "a", the old "b", and the tombstone that removed "b".
	wantDead := deadA + deadB + int64(tombstoneTotal)
	if stats.Dead != wantDead {
		t.Errorf("dead bytes = %d, want %d (old a %d + old b %d + tombstone %d)",
			stats.Dead, wantDead, deadA, deadB, tombstoneTotal)
	}
	// Live bytes must be the replacement plus "c", never the superseded pair.
	wantLive := int64(recordHeaderSize+len(replacement)) + recordHeaderSize + int64(c.lengths[2])
	if stats.Bytes[c.id] != wantLive {
		t.Errorf("live bytes = %d, want %d", stats.Bytes[c.id], wantLive)
	}
}

// TestScanLiveOnInsertOnlyFile is the baseline: with nothing superseded, every
// record is live and there are no dead bytes.
func TestScanLiveOnInsertOnlyFile(t *testing.T) {
	path := buildFile(t, 5)

	stats, err := ScanLive(path)
	if err != nil {
		t.Fatalf("ScanLive: %v", err)
	}
	if stats.Dead != 0 {
		t.Errorf("dead bytes on an insert-only file = %d, want 0", stats.Dead)
	}
	total := 0
	for _, n := range stats.Documents {
		total += n
	}
	if total != 5 {
		t.Errorf("live documents = %d, want 5", total)
	}
}
