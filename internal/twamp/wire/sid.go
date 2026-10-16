package wire

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Akkanbe/gotwamp"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// SID is a TWAMP session identifier (RFC 4656 §3.5).
type SID = gotwamp.SID

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
