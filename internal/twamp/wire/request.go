package wire

import (
	"encoding/binary"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// RequestTWSessionSize is the length of a Request-TW-Session.
const RequestTWSessionSize = 112

// IP versions for the IPVN field of Request-TW-Session.
const (
	IPVN4 = 4
	IPVN6 = 6
)

var errShortRequestTWSession = fmt.Errorf("request-tw-session: %w", ErrShortBuffer)

// RequestTWSession asks the server for one test session (RFC 5357 §3.5, with
// the layout of RFC 4656 §3.5 and the fields of RFC 6038 §4.2).
//
//	0 Command (5), 1 MBZ|IPVN, 2 Conf-Sender, 3 Conf-Receiver,
//	4 Number of Schedule Slots, 8 Number of Packets, 12 Sender Port,
//	14 Receiver Port, 16 Sender Address, 32 Receiver Address, 48 SID,
//	64 Padding Length, 68 Start Time, 76 Timeout, 84 Type-P Descriptor,
//	88 Octets to be reflected, 90 Length of padding to reflect, 92–95 MBZ,
//	96 HMAC
//
// The fields RFC 5357 requires to be zero (Conf-Sender, Conf-Receiver, the
// two counts, SID) are kept so that a server can reject a request that sets
// them.
type RequestTWSession struct {
	// IPVN is the IP version of both addresses: IPVN4 or IPVN6.
	IPVN uint8
	// ConfSender must be 0 in TWAMP.
	ConfSender uint8
	// ConfReceiver must be 0 in TWAMP.
	ConfReceiver uint8
	// ScheduleSlots must be 0 in TWAMP.
	ScheduleSlots uint32
	// Packets must be 0 in TWAMP.
	Packets uint32
	// SenderPort is the UDP port of the Session-Sender.
	SenderPort uint16
	// ReceiverPort is the UDP port asked of the Session-Reflector.
	ReceiverPort uint16
	// SenderAddress is the Session-Sender address. With IPVN4 only the
	// first 4 octets are used; the rest are written and decoded as zero.
	SenderAddress [16]byte
	// ReceiverAddress is the Session-Reflector address, encoded like SenderAddress.
	ReceiverAddress [16]byte
	// SID must be zero in a request.
	SID SID
	// PaddingLength is the padding the Session-Sender appends to every packet.
	PaddingLength uint32
	// StartTime is the earliest start of the session; zero starts at Start-Sessions.
	StartTime ntptime.Timestamp
	// Timeout is how long the reflector keeps reflecting after Stop-Sessions,
	// an interval in NTP format (see ntptime.FromDuration).
	Timeout ntptime.Timestamp
	// TypeP is the Type-P Descriptor, which in TWAMP carries the DSCP.
	TypeP TypeP
	// OctetsToReflect is the RFC 6038 "Octets to be reflected" field.
	OctetsToReflect uint16
	// PaddingToReflect is the RFC 6038 "Length of padding to reflect" field.
	PaddingToReflect uint16
}

// Append appends the encoded request to dst, with a zero HMAC.
func (m *RequestTWSession) Append(dst []byte) []byte {
	var b [RequestTWSessionSize]byte
	b[0] = byte(CommandRequestTWSession)
	b[1] = m.IPVN & 0x0F
	b[2] = m.ConfSender
	b[3] = m.ConfReceiver
	binary.BigEndian.PutUint32(b[4:], m.ScheduleSlots)
	binary.BigEndian.PutUint32(b[8:], m.Packets)
	binary.BigEndian.PutUint16(b[12:], m.SenderPort)
	binary.BigEndian.PutUint16(b[14:], m.ReceiverPort)
	putAddress(b[16:32], m.IPVN, &m.SenderAddress)
	putAddress(b[32:48], m.IPVN, &m.ReceiverAddress)
	copy(b[48:64], m.SID[:])
	binary.BigEndian.PutUint32(b[64:], m.PaddingLength)
	binary.BigEndian.PutUint64(b[68:], uint64(m.StartTime))
	binary.BigEndian.PutUint64(b[76:], uint64(m.Timeout))
	binary.BigEndian.PutUint32(b[84:], uint32(m.TypeP))
	binary.BigEndian.PutUint16(b[88:], m.OctetsToReflect)
	binary.BigEndian.PutUint16(b[90:], m.PaddingToReflect)
	return append(dst, b[:]...)
}

// Decode decodes a request from the first RequestTWSessionSize octets of b.
// It fails with ErrUnexpectedCommand unless octet 0 is 5; the server answers
// that with Accept-Session AcceptNotSupported.
func (m *RequestTWSession) Decode(b []byte) error {
	if len(b) < RequestTWSessionSize {
		return errShortRequestTWSession
	}
	if c := Command(b[0]); c != CommandRequestTWSession {
		return fmt.Errorf("request-tw-session: %v: %w", c, ErrUnexpectedCommand)
	}
	m.IPVN = b[1] & 0x0F
	m.ConfSender = b[2]
	m.ConfReceiver = b[3]
	m.ScheduleSlots = binary.BigEndian.Uint32(b[4:])
	m.Packets = binary.BigEndian.Uint32(b[8:])
	m.SenderPort = binary.BigEndian.Uint16(b[12:])
	m.ReceiverPort = binary.BigEndian.Uint16(b[14:])
	getAddress(&m.SenderAddress, m.IPVN, b[16:32])
	getAddress(&m.ReceiverAddress, m.IPVN, b[32:48])
	copy(m.SID[:], b[48:64])
	m.PaddingLength = binary.BigEndian.Uint32(b[64:])
	m.StartTime = ntptime.Timestamp(binary.BigEndian.Uint64(b[68:]))
	m.Timeout = ntptime.Timestamp(binary.BigEndian.Uint64(b[76:]))
	m.TypeP = TypeP(binary.BigEndian.Uint32(b[84:]))
	m.OctetsToReflect = binary.BigEndian.Uint16(b[88:])
	m.PaddingToReflect = binary.BigEndian.Uint16(b[90:])
	return nil
}

// putAddress writes an address field. For IPv4 the 12 trailing octets MUST
// be zero (RFC 4656 §3.5; Junos rejects requests that set them).
func putAddress(dst []byte, ipvn uint8, a *[16]byte) {
	if ipvn == IPVN4 {
		copy(dst[:4], a[:4])
		return
	}
	copy(dst, a[:])
}

// getAddress reads an address field. For IPv4 the 12 trailing octets are
// ignored, as RFC 4656 §3.5 requires.
func getAddress(a *[16]byte, ipvn uint8, src []byte) {
	if ipvn == IPVN4 {
		*a = [16]byte{}
		copy(a[:4], src[:4])
		return
	}
	copy(a[:], src)
}

// TypeP is the Type-P Descriptor of Request-TW-Session. TWAMP uses only its
// DSCP form: two zero bits, then the 6-bit DSCP, then 24 zero bits
// (RFC 4656 §3.5, RFC 5357 §3.5).
type TypeP uint32

// TypePFromDSCP returns the descriptor for dscp. Only its low 6 bits are used.
func TypePFromDSCP(dscp uint8) TypeP { return TypeP(dscp&0x3F) << 24 }

// DSCP returns the DSCP the descriptor carries, and false if any bit outside
// the DSCP is set, which a TWAMP server answers with AcceptNotSupported.
func (t TypeP) DSCP() (uint8, bool) {
	return uint8(t>>24) & 0x3F, t&^(0x3F<<24) == 0
}
