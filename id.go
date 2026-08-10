package nosqlite

// id.go generates document ids.
//
// An _id is 16 bytes rendered as 32 lowercase hex characters:
//
//	 8 bytes  big-endian Unix nanoseconds
//	 8 bytes  crypto/rand
//
// Time first means ids sort by creation order, which makes them a cheap default
// sort and, once range scans exist, a range-scannable key. The 64 random bits
// make collisions a non-issue — which is why the _id table in index.go is built
// lazily and only for caller-supplied ids.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// newID returns a fresh document id.
func newID() (string, error) {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[0:8], uint64(time.Now().UnixNano()))
	// crypto/rand.Read fills the slice with cryptographically secure random
	// bytes. It only fails if the OS entropy source is broken, which is worth
	// reporting rather than papering over.
	if _, err := rand.Read(raw[8:16]); err != nil {
		return "", fmt.Errorf("nosqlite: generating _id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
