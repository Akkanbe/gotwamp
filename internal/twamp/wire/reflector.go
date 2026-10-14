package wire

import (
	"encoding/binary"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// ReflectorDSCPECNHeaderSize is the unauthenticated Session-Reflector header
// when DSCP and ECN Monitoring (RFC 7750) is in use: S-DSCP-ECN at offset 41,
// MBZ at 42–43, padding from 44.
const ReflectorDSCPECNHeaderSize = 44

var errShortReflectorPacket = fmt.Errorf("reflector packet: %w", ErrShortBuffer)

// ReflectorPacket is an unauthenticated Session-Reflector packet
// (RFC 5357 §4.2.1).
//
//	0 Sequence Number, 4 Timestamp, 12 Error Estimate, 14 MBZ,
//	16 Receive Timestamp, 24 Sender Sequence Number, 28 Sender Timestamp,
//	36 Sender Error Estimate, 38 MBZ, 40 Sender TTL, 41 Packet Padding
//
// With HasDSCPECN the RFC 7750 layout applies: 41 S-DSCP-ECN, 42 MBZ,
// 44 Packet Padding.
type ReflectorPacket struct {
	// Seq is the reflector's own per-session counter, or the sender's
	// Sequence Number for a stateless TWAMP Light reflector.
	Seq uint32
	// Timestamp is T3, the reflector's send time.
	Timestamp ntptime.Timestamp
	// ErrorEstimate is the reflector's clock error; it covers T2 and T3.
	ErrorEstimate errest.Estimate
	// ReceiveTimestamp is T2, the reflector's receive time.
	ReceiveTimestamp ntptime.Timestamp
	// SenderSeq is the Sequence Number of the reflected packet.
	SenderSeq uint32
	// SenderTimestamp is T1 copied from the reflected packet.
	SenderTimestamp ntptime.Timestamp
	// SenderErrorEstimate is copied from the reflected packet.
	SenderErrorEstimate errest.Estimate
	// SenderTTL is the TTL or Hop Limit of the reflected packet, 255 when
	// the reflector cannot read it.
	SenderTTL uint8
	// HasDSCPECN selects the RFC 7750 layout. Decode and Reflect read it as
	// an input and leave it unchanged.
	HasDSCPECN bool
	// DSCPECN is the RFC 7750 S-DSCP-ECN octet, on the wire only with HasDSCPECN.
	DSCPECN DSCPECN
	// SymmetricalSize selects the RFC 6038 Symmetrical Size reflection: the
	// Session-Sender header already spans the reflector header, so Reflect
	// leaves the padding in place and the reply is as long as the request.
	// It is an input of Reflect only.
	SymmetricalSize bool
	// SSID is the STAMP Session-Sender Identifier at offset 14 (RFC 8972
	// §3), which TWAMP leaves MBZ: ReflectSTAMP copies it from the request,
	// and Decode, which ignores MBZ octets, leaves it alone (see STAMPSSID).
	SSID uint16
	// Padding follows the header. Decode and Reflect make it alias the buffer.
	Padding []byte
}

// headerSize returns the header length of the layout p uses.
func (p *ReflectorPacket) headerSize() int {
	if p.HasDSCPECN {
		return ReflectorDSCPECNHeaderSize
	}
	return ReflectorHeaderSize
}

// Append appends the encoded packet, header and Padding, to dst.
func (p *ReflectorPacket) Append(dst []byte) []byte {
	var b [ReflectorDSCPECNHeaderSize]byte
	n := p.headerSize()
	p.putHeader(b[:n])
	return append(append(dst, b[:n]...), p.Padding...)
}

// putHeader writes the header of the layout p uses into b.
//
//declscope:package // ReflectSTAMP writes the fields STAMP shares with TWAMP through it
func (p *ReflectorPacket) putHeader(b []byte) {
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[4:], uint64(p.Timestamp))
	binary.BigEndian.PutUint16(b[12:], uint16(p.ErrorEstimate))
	binary.BigEndian.PutUint16(b[14:], p.SSID)
	binary.BigEndian.PutUint64(b[16:], uint64(p.ReceiveTimestamp))
	binary.BigEndian.PutUint32(b[24:], p.SenderSeq)
	binary.BigEndian.PutUint64(b[28:], uint64(p.SenderTimestamp))
	binary.BigEndian.PutUint16(b[36:], uint16(p.SenderErrorEstimate))
	b[38], b[39] = 0, 0
	b[40] = p.SenderTTL
	if p.HasDSCPECN {
		b[41], b[42], b[43] = byte(p.DSCPECN), 0, 0
	}
}

// Decode decodes a packet of at least the header size of the layout
// HasDSCPECN selects. Padding aliases b.
func (p *ReflectorPacket) Decode(b []byte) error {
	n := p.headerSize()
	if len(b) < n {
		return errShortReflectorPacket
	}
	p.Seq = binary.BigEndian.Uint32(b[0:])
	p.Timestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[4:]))
	p.ErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[12:]))
	p.ReceiveTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[16:]))
	p.SenderSeq = binary.BigEndian.Uint32(b[24:])
	p.SenderTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[28:]))
	p.SenderErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[36:]))
	p.SenderTTL = b[40]
	if p.HasDSCPECN {
		p.DSCPECN = DSCPECN(b[41])
	}
	p.Padding = b[n:]
	return nil
}

// Reflect turns the unauthenticated Session-Sender packet in b[:n] into the
// reply in place and returns the reply length, max(n, header size)
// (SPEC §6.4 steps 4–5).
//
// The header takes Seq, Timestamp, ErrorEstimate, ReceiveTimestamp,
// SenderTTL and DSCPECN from p; the Sender fields are copied from the
// received packet and also stored in p. The sender's padding moves to the
// reply's padding, and the highest-numbered octets that no longer fit are
// dropped (RFC 5357 §4.2.1), so the reply is as long as the request unless
// the request is shorter than the reply header. With SymmetricalSize the
// padding stays where it is instead (RFC 6038 §5.2.2). p.Padding is set to
// the reply's padding, which aliases b. Offset 14 carries p.SSID, which is
// zero (MBZ) for TWAMP.
//
// b must hold the reply: len(b) ≥ max(n, header size). Write the final T3
// with PutTimestamp just before sending.
func (p *ReflectorPacket) Reflect(b []byte, n int) (int, error) {
	hdr := p.headerSize()
	if n < SenderHeaderSize || n > len(b) || len(b) < hdr {
		return 0, errShortReflectorPacket
	}
	p.SenderSeq = binary.BigEndian.Uint32(b[0:])
	p.SenderTimestamp = ntptime.Timestamp(binary.BigEndian.Uint64(b[4:]))
	p.SenderErrorEstimate = errest.Estimate(binary.BigEndian.Uint16(b[12:]))
	reply := max(n, hdr)
	if !p.SymmetricalSize {
		// Move the padding before writing the header, which overlaps it.
		copy(b[hdr:reply], b[SenderHeaderSize:])
	}
	p.putHeader(b[:hdr])
	p.Padding = b[hdr:reply]
	return reply, nil
}

// DSCPECN is the RFC 7750 S-DSCP-ECN octet: the DSCP (6 bits) and ECN
// (2 bits) of the packet the reflector received.
type DSCPECN uint8

// NewDSCPECN packs dscp and ecn; only their low 6 and 2 bits are used.
func NewDSCPECN(dscp, ecn uint8) DSCPECN { return DSCPECN(dscp<<2 | ecn&0x03) }

// DSCP returns the 6-bit DSCP.
func (v DSCPECN) DSCP() uint8 { return uint8(v) >> 2 }

// ECN returns the 2-bit ECN codepoint.
func (v DSCPECN) ECN() uint8 { return uint8(v) & 0x03 }
