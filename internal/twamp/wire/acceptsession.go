package wire

import (
	"encoding/binary"
	"fmt"
)

// AcceptSessionSize is the length of an Accept-Session.
const AcceptSessionSize = 48

var errShortAcceptSession = fmt.Errorf("accept-session: %w", ErrShortBuffer)

// AcceptSession is the server's answer to Request-TW-Session (RFC 4656 §3.5,
// RFC 5357 §3.5, RFC 6038 §4.3).
//
//	0 Accept, 1 MBZ, 2 Port, 4 SID, 20 Reflected octets, 22 Server octets,
//	24–31 MBZ, 32 HMAC
type AcceptSession struct {
	// Accept is AcceptOK when the session is accepted.
	Accept Accept
	// Port is the reflector's UDP port: the requested Receiver Port or an
	// alternate one. It must be 0 when Accept is not AcceptOK.
	Port uint16
	// SID identifies the session.
	SID SID
	// ReflectedOctets is the RFC 6038 "Reflected octets" field.
	ReflectedOctets uint16
	// ServerOctets is the RFC 6038 "Server octets" field.
	ServerOctets uint16
}

// Append appends the encoded message to dst, with a zero HMAC.
func (m *AcceptSession) Append(dst []byte) []byte {
	var b [AcceptSessionSize]byte
	b[0] = byte(m.Accept)
	binary.BigEndian.PutUint16(b[2:], m.Port)
	copy(b[4:20], m.SID[:])
	binary.BigEndian.PutUint16(b[20:], m.ReflectedOctets)
	binary.BigEndian.PutUint16(b[22:], m.ServerOctets)
	return append(dst, b[:]...)
}

// Decode decodes an Accept-Session from the first AcceptSessionSize octets of b.
func (m *AcceptSession) Decode(b []byte) error {
	if len(b) < AcceptSessionSize {
		return errShortAcceptSession
	}
	m.Accept = Accept(b[0])
	m.Port = binary.BigEndian.Uint16(b[2:])
	copy(m.SID[:], b[4:20])
	m.ReflectedOctets = binary.BigEndian.Uint16(b[20:])
	m.ServerOctets = binary.BigEndian.Uint16(b[22:])
	return nil
}
