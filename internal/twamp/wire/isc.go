package wire

import (
	"encoding/binary"
	"fmt"
)

// This file is the RFC 5938 Individual Session Control messages, which start
// and stop sessions by SID.

// SessionControl is one of the RFC 5938 messages that list sessions by SID:
// Start-N-Sessions (command 7) and Stop-N-Sessions (9) from the client,
// Start-N-Ack (8) and Stop-N-Ack (10) from the server (RFC 5938 §3.2–§3.5).
//
//	0 Command, 1 Accept (acks; MBZ in the others), 2–11 MBZ,
//	12 Number of Sessions, 16 the SIDs, 16 octets each, then the HMAC
type SessionControl struct {
	// Command is CommandStartNSessions, CommandStartNAck,
	// CommandStopNSessions or CommandStopNAck.
	Command Command
	// Accept is the outcome for SIDs, in the acks only.
	Accept Accept
	// SIDs are the sessions the message is about.
	SIDs []SID
}

// SessionControlSize returns the length of a SessionControl message listing
// n sessions.
func SessionControlSize(n int) int { return 2*ControlHeaderSize + len(SID{})*n }

// SessionControlCount returns the Number of Sessions in the first
// ControlHeaderSize octets of a SessionControl message, which tells its
// length.
func SessionControlCount(header []byte) uint32 { return binary.BigEndian.Uint32(header[12:]) }

// IsSessionControl reports whether c is one of the RFC 5938 commands.
func (c Command) IsSessionControl() bool {
	return c >= CommandStartNSessions && c <= CommandStopNAck
}

// isAck reports whether c is a server acknowledgement, which has an Accept.
func (c Command) isAck() bool { return c == CommandStartNAck || c == CommandStopNAck }

var errShortSessionControl = fmt.Errorf("session control message: %w", ErrShortBuffer)

// Append appends the encoded message to dst, with a zero HMAC.
func (m *SessionControl) Append(dst []byte) []byte {
	start := len(dst)
	dst = append(dst, make([]byte, SessionControlSize(len(m.SIDs)))...)
	b := dst[start:]
	b[0] = byte(m.Command)
	if m.Command.isAck() {
		b[1] = byte(m.Accept)
	}
	binary.BigEndian.PutUint32(b[12:], uint32(len(m.SIDs))) //nolint:gosec // G115: a list held in memory
	for i, sid := range m.SIDs {
		copy(b[ControlHeaderSize+len(sid)*i:], sid[:])
	}
	return dst
}

// Decode decodes a message whose length its Number of Sessions gives. The
// SIDs are copied out of b.
func (m *SessionControl) Decode(b []byte) error {
	if len(b) < ControlHeaderSize {
		return errShortSessionControl
	}
	cmd := Command(b[0])
	if !cmd.IsSessionControl() {
		return fmt.Errorf("session control message: %w %v", ErrUnexpectedCommand, cmd)
	}
	n := SessionControlCount(b)
	if uint64(len(b)) < uint64(SessionControlSize(0))+uint64(len(SID{}))*uint64(n) {
		return errShortSessionControl
	}
	m.Command, m.Accept = cmd, 0
	if cmd.isAck() {
		m.Accept = Accept(b[1])
	}
	m.SIDs = make([]SID, n)
	for i := range m.SIDs {
		copy(m.SIDs[i][:], b[ControlHeaderSize+len(SID{})*i:])
	}
	return nil
}
