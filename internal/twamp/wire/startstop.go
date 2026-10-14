package wire

import (
	"encoding/binary"
	"fmt"
)

// Lengths of the session start and stop messages.
const (
	StartSessionsSize = 32
	StartAckSize      = 32
	StopSessionsSize  = 32
)

var (
	errShortStartSessions = fmt.Errorf("start-sessions: %w", ErrShortBuffer)
	errShortStartAck      = fmt.Errorf("start-ack: %w", ErrShortBuffer)
	errShortStopSessions  = fmt.Errorf("stop-sessions: %w", ErrShortBuffer)
)

// StartSessions starts every accepted session (RFC 4656 §3.7).
//
//	0 Command (2), 1–15 MBZ, 16 HMAC
type StartSessions struct{}

// Append appends the encoded message to dst, with a zero HMAC.
func (m *StartSessions) Append(dst []byte) []byte {
	var b [StartSessionsSize]byte
	b[0] = byte(CommandStartSessions)
	return append(dst, b[:]...)
}

// Decode checks that b starts with a Start-Sessions message.
func (m *StartSessions) Decode(b []byte) error {
	if len(b) < StartSessionsSize {
		return errShortStartSessions
	}
	if c := Command(b[0]); c != CommandStartSessions {
		return fmt.Errorf("start-sessions: %v: %w", c, ErrUnexpectedCommand)
	}
	return nil
}

// StartAck answers Start-Sessions (RFC 4656 §3.7).
//
//	0 Accept, 1–15 MBZ, 16 HMAC
type StartAck struct {
	// Accept is AcceptOK when the sessions start.
	Accept Accept
}

// Append appends the encoded message to dst, with a zero HMAC.
func (m *StartAck) Append(dst []byte) []byte {
	var b [StartAckSize]byte
	b[0] = byte(m.Accept)
	return append(dst, b[:]...)
}

// Decode decodes a Start-Ack from the first StartAckSize octets of b.
func (m *StartAck) Decode(b []byte) error {
	if len(b) < StartAckSize {
		return errShortStartAck
	}
	m.Accept = Accept(b[0])
	return nil
}

// StopSessions stops every started session; TWAMP has no reply to it
// (RFC 5357 §3.8).
//
//	0 Command (3), 1 Accept, 2–3 MBZ, 4 Number of Sessions, 8–15 MBZ, 16 HMAC
type StopSessions struct {
	// Accept is AcceptOK for a normal stop; otherwise the results are invalid.
	Accept Accept
	// Sessions must equal the number of sessions started and not yet stopped.
	Sessions uint32
}

// Append appends the encoded message to dst, with a zero HMAC.
func (m *StopSessions) Append(dst []byte) []byte {
	var b [StopSessionsSize]byte
	b[0] = byte(CommandStopSessions)
	b[1] = byte(m.Accept)
	binary.BigEndian.PutUint32(b[4:], m.Sessions)
	return append(dst, b[:]...)
}

// Decode decodes a Stop-Sessions from the first StopSessionsSize octets of b.
func (m *StopSessions) Decode(b []byte) error {
	if len(b) < StopSessionsSize {
		return errShortStopSessions
	}
	if c := Command(b[0]); c != CommandStopSessions {
		return fmt.Errorf("stop-sessions: %v: %w", c, ErrUnexpectedCommand)
	}
	m.Accept = Accept(b[1])
	m.Sessions = binary.BigEndian.Uint32(b[4:])
	return nil
}
