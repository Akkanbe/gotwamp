package wire

import (
	"encoding/binary"

	"github.com/Akkanbe/gotwamp"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// Layout sizes of TWAMP-Test packets, in octets. The authenticated sizes
// follow RFC 5357 as corrected by EID 5045 and EID 5046.
const (
	// SenderHeaderSize is the unauthenticated Session-Sender packet without padding.
	SenderHeaderSize = gotwamp.SenderHeaderSize
	// ReflectorHeaderSize is the unauthenticated Session-Reflector packet without padding.
	ReflectorHeaderSize = gotwamp.ReflectorHeaderSize
	// AuthSenderHeaderSize is the authenticated or encrypted Session-Sender packet without padding.
	AuthSenderHeaderSize = gotwamp.AuthSenderHeaderSize
	// AuthReflectorHeaderSize is the authenticated or encrypted Session-Reflector packet without padding.
	AuthReflectorHeaderSize = gotwamp.AuthReflectorHeaderSize
	// hmacSize is the length of every HMAC field, in control messages and
	// test packets.
	//
	//declscope:package // the control and test-packet cryptography use it
	hmacSize = 16
	// DefaultPaddingLength makes unauthenticated packets the same size in both
	// directions (SPEC appendix A).
	DefaultPaddingLength = gotwamp.DefaultPaddingLength
)

// SecurityMode is the canonical public value.
type SecurityMode = gotwamp.SecurityMode

// SecurityUnauthenticated is the canonical public constant.
const SecurityUnauthenticated = gotwamp.SecurityUnauthenticated

// SecurityAuthenticated is the canonical public constant.
const SecurityAuthenticated = gotwamp.SecurityAuthenticated

// SecurityEncrypted is the canonical public constant.
const SecurityEncrypted = gotwamp.SecurityEncrypted

// SecurityMixed is the canonical public constant.
const SecurityMixed = gotwamp.SecurityMixed

// AuthSenderPacket is the plaintext of an authenticated or encrypted
// Session-Sender packet (RFC 4656 §4.1.2): Sequence Number at 0, MBZ 4–15,
// Timestamp at 16, Error Estimate at 24, MBZ 26–31, HMAC at 32, padding from
// 48. TestCrypto.SealSender and OpenSender protect it.
type AuthSenderPacket struct {
	Seq           uint32
	Timestamp     ntptime.Timestamp
	ErrorEstimate errest.Estimate
	HMAC          [hmacSize]byte
	Padding       []byte
}

// AuthReflectorPacket is the plaintext of an authenticated or encrypted
// Session-Reflector packet (RFC 5357 §4.2.1, EID 5045): Sequence Number at 0,
// Timestamp at 16, Error Estimate at 24, Receive Timestamp at 32, Sender
// Sequence Number at 48, Sender Timestamp at 64, Sender Error Estimate at 72,
// Sender TTL at 80, HMAC at 96, padding from 112; every other octet is MBZ.
// With HasDSCPECN (RFC 7750) the S-DSCP-ECN octet follows at 81.
// TestCrypto.SealReflector and OpenReflector protect it.
type AuthReflectorPacket struct {
	Seq                 uint32
	Timestamp           ntptime.Timestamp
	ErrorEstimate       errest.Estimate
	ReceiveTimestamp    ntptime.Timestamp
	SenderSeq           uint32
	SenderTimestamp     ntptime.Timestamp
	SenderErrorEstimate errest.Estimate
	SenderTTL           uint8
	HasDSCPECN          bool
	DSCPECN             DSCPECN
	// SymmetricalSize leaves the padding in place in Reflect (RFC 6038
	// §5.2.2): the sender header already spans the 112 octets.
	SymmetricalSize bool
	HMAC            [hmacSize]byte
	Padding         []byte
}

// PutHeader writes the AuthSenderHeaderSize-octet plaintext header into b,
// MBZ octets cleared and the HMAC field from p, and leaves the padding after
// it untouched. p.Padding is not used.
func (p *AuthSenderPacket) PutHeader(b []byte) error {
	if len(b) < AuthSenderHeaderSize {
		return errShortAuthSender
	}
	clear(b[:AuthSenderHeaderSize])
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[16:], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[24:], uint16(p.ErrorEstimate))
	copy(b[32:48], p.HMAC[:])
	return nil
}

// Decode decodes the plaintext of a packet of at least AuthSenderHeaderSize
// octets. Padding aliases b.
func (p *AuthSenderPacket) Decode(b []byte) error {
	if len(b) < AuthSenderHeaderSize {
		return errShortAuthSender
	}
	p.Seq = binary.BigEndian.Uint32(b[0:])
	p.Timestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[16:]))
	p.ErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[24:]))
	copy(p.HMAC[:], b[32:48])
	p.Padding = b[AuthSenderHeaderSize:]
	return nil
}

// PutAuthTimestamp writes ts into the Timestamp field of an authenticated or
// encrypted Session-Sender or Session-Reflector plaintext, which both hold it
// at offset 16. In encrypted mode the field is protected: write it before
// sealing.
func PutAuthTimestamp(b []byte, ts ntptime.Timestamp) error {
	if len(b) < AuthSenderHeaderSize {
		return errShortAuthSender
	}
	binary.BigEndian.PutUint64(b[16:], uint64(ts))
	return nil
}

func (p *AuthReflectorPacket) putHeader(b []byte) {
	clear(b[:AuthReflectorHeaderSize])
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[16:], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[24:], uint16(p.ErrorEstimate))
	binary.BigEndian.PutUint64(b[32:], uint64(p.ReceiveTimestamp))
	binary.BigEndian.PutUint32(b[48:], p.SenderSeq)
	binary.BigEndian.PutUint64(b[64:], uint64(p.SenderTimestamp))
	binary.BigEndian.PutUint16(b[72:], uint16(p.SenderErrorEstimate))
	b[80] = p.SenderTTL
	if p.HasDSCPECN {
		b[81] = byte(p.DSCPECN)
	}
	copy(b[96:112], p.HMAC[:])
}

// Append appends the encoded plaintext, header and Padding, to dst.
func (p *AuthReflectorPacket) Append(dst []byte) []byte {
	var b [AuthReflectorHeaderSize]byte
	p.putHeader(b[:])
	return append(append(dst, b[:]...), p.Padding...)
}

// Decode decodes the plaintext of a packet of at least
// AuthReflectorHeaderSize octets; HasDSCPECN is an input. Padding aliases b.
func (p *AuthReflectorPacket) Decode(b []byte) error {
	if len(b) < AuthReflectorHeaderSize {
		return errShortAuthReflector
	}
	p.Seq = binary.BigEndian.Uint32(b[0:])
	p.Timestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[16:]))
	p.ErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[24:]))
	p.ReceiveTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[32:]))
	p.SenderSeq = binary.BigEndian.Uint32(b[48:])
	p.SenderTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[64:]))
	p.SenderErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[72:]))
	p.SenderTTL = b[80]
	if p.HasDSCPECN {
		p.DSCPECN = DSCPECN(b[81])
	}
	copy(p.HMAC[:], b[96:112])
	p.Padding = b[AuthReflectorHeaderSize:]
	return nil
}

// Reflect turns the opened plaintext of an authenticated or encrypted
// Session-Sender packet in b[:n] into the reply plaintext in place, like
// ReflectorPacket.Reflect: the Sender fields come from the packet, the rest
// of the header from p, and the padding moves behind the 112-octet header
// with its highest-numbered octets dropped, so the reply is max(n, 112)
// octets; with SymmetricalSize it stays in place. The HMAC field is
// cleared for SealReflector. b must hold the reply.
func (p *AuthReflectorPacket) Reflect(b []byte, n int) (int, error) {
	if n < AuthSenderHeaderSize || n > len(b) || len(b) < AuthReflectorHeaderSize {
		return 0, errShortAuthReflector
	}
	p.SenderSeq = binary.BigEndian.Uint32(b[0:])
	p.SenderTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[16:]))
	p.SenderErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[24:]))
	p.HMAC = [hmacSize]byte{}
	reply := max(n, AuthReflectorHeaderSize)
	if !p.SymmetricalSize {
		copy(b[AuthReflectorHeaderSize:reply], b[AuthSenderHeaderSize:n])
	}
	p.putHeader(b)
	p.Padding = b[AuthReflectorHeaderSize:reply]
	return reply, nil
}
