package wire

import (
	"encoding/binary"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// This file is the unauthenticated STAMP base packet (RFC 8762 §4.2.1,
// §4.3.1) with the SSID of RFC 8972 §3, and the walk over the RFC 8972 TLVs
// that follow it. STAMP packets are TWAMP-Test packets on the wire (RFC 8972
// §3): the Session-Reflector fields are at the same offsets, offset 14–15
// carries the SSID, and both base packets are 44 octets.

// STAMPBaseSize is the unauthenticated STAMP base packet, Session-Sender and
// Session-Reflector alike. Octets after it are TLVs (RFC 8972 §4).
const STAMPBaseSize = 44

// Flags of a STAMP TLV (RFC 8972 §4.1).
const (
	// TLVUnrecognized (U) is set by the Session-Sender on every TLV and by
	// the Session-Reflector on the TLVs it did not understand.
	TLVUnrecognized = 0x80
	// TLVMalformed (M) is set by the Session-Reflector on a TLV whose length
	// runs past the packet; it processes no further TLV. The I flag (0x20,
	// failed HMAC of the extensions) does not arise without the HMAC TLV.
	TLVMalformed = 0x40
)

// tlvExtraPadding is the type of the Extra Padding TLV (RFC 8972 §4.2).
const tlvExtraPadding = 1

// tlvHeaderSize is the Flags, Type and Length of a TLV.
const tlvHeaderSize = 4

var errShortSTAMPPacket = fmt.Errorf("stamp packet: %w", ErrShortBuffer)

// STAMPSenderPacket is the unauthenticated STAMP Session-Sender base packet
// (RFC 8762 §4.2.1, RFC 8972 §3).
//
//	0 Sequence Number, 4 Timestamp, 12 Error Estimate, 14 SSID, 16–43 MBZ
type STAMPSenderPacket struct {
	Seq           uint32
	Timestamp     ntptime.Timestamp
	ErrorEstimate errest.Estimate
	// SSID identifies the session to the reflector; 0 means none.
	SSID uint16
}

// PutHeader writes the STAMPBaseSize-octet base packet into b, MBZ octets
// cleared, and leaves the TLVs after it untouched.
func (p *STAMPSenderPacket) PutHeader(b []byte) error {
	if len(b) < STAMPBaseSize {
		return errShortSTAMPPacket
	}
	clear(b[:STAMPBaseSize])
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[4:], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[12:], uint16(p.ErrorEstimate))
	binary.BigEndian.PutUint16(b[14:], p.SSID)
	return nil
}

// PutExtraPadding writes an Extra Padding TLV filling b, whose value is left
// as it is (RFC 8972 §4.2): the Session-Sender sets the U flag. b must hold
// at least the 4-octet TLV header and at most 65535 octets of value.
func PutExtraPadding(b []byte) error {
	if len(b) < tlvHeaderSize || len(b)-tlvHeaderSize > 0xffff {
		return fmt.Errorf("extra padding tlv of %d octets: %w", len(b), ErrShortBuffer)
	}
	b[0], b[1] = TLVUnrecognized, tlvExtraPadding
	binary.BigEndian.PutUint16(b[2:], uint16(len(b)-tlvHeaderSize)) //nolint:gosec // G115: bounded above
	return nil
}

// STAMPSSID returns the SSID of a STAMP packet, sender or reflector (RFC
// 8972 §3), which is MBZ in TWAMP. It is 0 for a packet too short to hold it.
func STAMPSSID(b []byte) uint16 {
	if len(b) < SenderHeaderSize+2 {
		return 0
	}
	return binary.BigEndian.Uint16(b[SenderHeaderSize:])
}

// ReflectSTAMP turns the STAMP Session-Sender packet in b[:n] into the
// Session-Reflector reply in place (RFC 8762 §4.3.1) and returns its length,
// max(n, STAMPBaseSize). Like Reflect, the header takes Seq, Timestamp,
// ErrorEstimate, ReceiveTimestamp and SenderTTL from p and the Sender
// fields from the packet; the SSID is copied from offset 14 (RFC 8972 §3)
// and offsets 41–43 are MBZ, so HasDSCPECN does not apply. A request shorter
// than the base gets the base packet; the octets after the base stay where
// they are, to be reflected (RFC 8762 §4.6). p.Padding is set to them; walk
// them with MarkSTAMPTLVs.
func (p *ReflectorPacket) ReflectSTAMP(b []byte, n int) (int, error) {
	if n < SenderHeaderSize || n > len(b) || len(b) < STAMPBaseSize {
		return 0, errShortSTAMPPacket
	}
	p.SenderSeq = binary.BigEndian.Uint32(b[0:])
	p.SenderTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[4:]))
	p.SenderErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[12:]))
	p.SSID = STAMPSSID(b[:n])
	reply := max(n, STAMPBaseSize)
	clear(b[n:reply])
	h := *p
	h.HasDSCPECN = false
	h.putHeader(b[:ReflectorHeaderSize])
	clear(b[ReflectorHeaderSize:STAMPBaseSize])
	p.Padding = b[STAMPBaseSize:reply]
	return reply, nil
}

// tlvSenderFlags masks the flags a Session-Sender sets on every TLV: U set,
// M and I clear (RFC 8972 §4.1).
const (
	tlvSenderMask  = 0xe0
	tlvSenderFlags = TLVUnrecognized
)

// MarkSTAMPTLVs walks the TLVs after a STAMP base packet in a reply, which
// the reflector copies back, and marks them for a reflector that implements
// none of them (RFC 8972 §4): the U flag on each, and the M flag on a TLV
// that runs past the packet, which ends the walk. Octets too few for a TLV
// header are left as they are. It returns the number of TLVs it walked.
//
// The octets are taken for TLVs only when the first one carries the flags
// every sender sets (100xxxxx); otherwise they are the padding of a TWAMP
// Light sender and left alone, which is how SR Linux tells the two apart.
func MarkSTAMPTLVs(tlvs []byte) int {
	if len(tlvs) == 0 || tlvs[0]&tlvSenderMask != tlvSenderFlags {
		return 0
	}
	n := 0
	for len(tlvs) >= tlvHeaderSize {
		n++
		length := tlvHeaderSize + int(binary.BigEndian.Uint16(tlvs[2:]))
		if length > len(tlvs) {
			tlvs[0] |= TLVMalformed
			return n
		}
		tlvs[0] |= TLVUnrecognized
		tlvs = tlvs[length:]
	}
	return n
}
