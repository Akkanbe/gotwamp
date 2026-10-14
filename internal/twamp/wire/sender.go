package wire

import (
	"encoding/binary"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

var errShortSenderPacket = fmt.Errorf("sender packet: %w", ErrShortBuffer)

// SenderPacket is an unauthenticated Session-Sender packet (RFC 4656 §4.1.2,
// RFC 5357 §4.1.2).
//
//	0 Sequence Number, 4 Timestamp, 12 Error Estimate, 14 Packet Padding
type SenderPacket struct {
	// Seq counts packets of the session from 0.
	Seq uint32
	// Timestamp is T1, the send time.
	Timestamp ntptime.Timestamp
	// ErrorEstimate is the sender's clock error.
	ErrorEstimate errest.Estimate
	// Padding follows the header. Decode makes it alias the decoded buffer.
	Padding []byte
}

// Append appends the encoded packet, header and Padding, to dst.
func (p *SenderPacket) Append(dst []byte) []byte {
	var b [SenderHeaderSize]byte
	p.putHeader(b[:])
	return append(append(dst, b[:]...), p.Padding...)
}

// PutHeader writes the SenderHeaderSize-octet header into b and leaves the
// padding after it untouched, so a sender can fill the padding once per
// session. p.Padding is not used.
func (p *SenderPacket) PutHeader(b []byte) error {
	if len(b) < SenderHeaderSize {
		return errShortSenderPacket
	}
	p.putHeader(b)
	return nil
}

func (p *SenderPacket) putHeader(b []byte) {
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[4:], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[12:], uint16(p.ErrorEstimate))
}

// Decode decodes a packet of at least SenderHeaderSize octets. Every octet
// after the header is padding, which is how a Light reflector also serves
// STAMP senders (RFC 8762 §4.6). Padding aliases b.
func (p *SenderPacket) Decode(b []byte) error {
	if len(b) < SenderHeaderSize {
		return errShortSenderPacket
	}
	p.Seq = binary.BigEndian.Uint32(b[0:])
	p.Timestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[4:]))
	p.ErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[12:]))
	p.Padding = b[SenderHeaderSize:]
	return nil
}

// PutTimestamp writes ts into the Timestamp field of an unauthenticated
// Session-Sender or Session-Reflector packet, which both hold it at offset 4.
// It is meant for the last step before sending, so that T1 and T3 are taken
// as late as possible (SPEC §6.4, §6.5).
func PutTimestamp(b []byte, ts ntptime.Timestamp) error {
	if len(b) < SenderHeaderSize {
		return errShortSenderPacket
	}
	binary.BigEndian.PutUint64(b[4:], uint64(ts))
	return nil
}
