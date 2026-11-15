package wire

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// SID is a TWAMP session identifier (RFC 4656 §3.5).
type SID [16]byte

// NewSID builds a SID the way RFC 4656 §3.5 recommends: an IPv4 address of
// the server (or the last 4 octets of an IPv6 one), an NTP timestamp, and 4
// octets read from rand.
func NewSID(addr [4]byte, ts ntptime.Timestamp, rand io.Reader) (SID, error) {
	var s SID
	copy(s[0:4], addr[:])
	binary.BigEndian.PutUint64(s[4:12], uint64(ts))
	if _, err := io.ReadFull(rand, s[12:16]); err != nil {
		return SID{}, fmt.Errorf("sid: %w", err)
	}
	return s, nil
}

// IsZero reports whether every octet of s is zero, as Request-TW-Session requires.
func (s SID) IsZero() bool { return s == SID{} }

// String returns s in hexadecimal.
func (s SID) String() string { return hex.EncodeToString(s[:]) }

// MarshalText returns s in hexadecimal, so that log handlers and JSON
// encoders print a SID the way String does rather than as 16 numbers.
func (s SID) MarshalText() ([]byte, error) {
	b := make([]byte, hex.EncodedLen(len(s)))
	hex.Encode(b, s[:])
	return b, nil
}

// UnmarshalText reads the hexadecimal form MarshalText writes.
func (s *SID) UnmarshalText(text []byte) error {
	if hex.DecodedLen(len(text)) != len(s) {
		return fmt.Errorf("sid %q: want %d hexadecimal digits", text, 2*len(s))
	}
	if _, err := hex.Decode(s[:], text); err != nil {
		return fmt.Errorf("sid %q: %w", text, err)
	}
	return nil
}
