package gotwamp

import (
	"encoding/hex"
	"fmt"
)

// SID is a TWAMP session identifier (RFC 4656 §3.5).
type SID [16]byte

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
