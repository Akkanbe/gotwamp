// Package ulid makes the run IDs of gotwamp: ULIDs, 48 bits of Unix
// milliseconds and 80 random bits written as 26 Crockford base32
// characters, which sort by the time they were made.
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"time"
)

// alphabet is Crockford's base32.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// New returns the ULID of t with random bits from r; nil r means
// crypto/rand. A time before 1970 counts as 1970.
func New(t time.Time, r io.Reader) string {
	if r == nil {
		r = rand.Reader
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(max(t.UnixMilli(), 0))<<16) //nolint:gosec // G115: non-negative
	_, _ = io.ReadFull(r, b[6:])
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}
