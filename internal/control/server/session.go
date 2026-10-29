package server

import (
	"errors"
	"net/netip"
	"time"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// SessionSpec describes an accepted test session to the SessionHandler.
type SessionSpec struct {
	// SID identifies the session.
	SID wire.SID
	// Local is the address and port to bind the reflector socket to.
	Local netip.AddrPort
	// Sender is the Session-Sender: the Sender Address of the request, or
	// the control peer when it was zero, and the Sender Port.
	Sender netip.AddrPort
	// ControlPeer is the Control-Client end of the control connection.
	ControlPeer netip.AddrPort
	// DSCP is the Type-P DSCP the replies must carry.
	DSCP uint8
	// PaddingLength is the requested Packet Padding length.
	PaddingLength uint32
	// Timeout is how long to keep reflecting after Stop-Sessions.
	Timeout time.Duration
	// StartTime is the requested Start Time; zero means at Start-Sessions.
	StartTime time.Time
	// DSCPECNMonitoring selects the RFC 7750 reply layout, with the received
	// DSCP and ECN at offset 41. It is set when DSCP and ECN Monitoring (Modes
	// bit 8) is negotiated, which this server does not offer yet (SPEC §13).
	DSCPECNMonitoring bool
	// Security is the mode of the test packets: unauthenticated (also for a
	// connection in mixed mode), authenticated or encrypted.
	Security wire.SecurityMode
	// TestKeys are the keys of the test packets in the authenticated and
	// encrypted modes, derived from the control session keys and the SID
	// (RFC 5357 §4.2.1).
	TestKeys wire.TestKeys
	// Layout is the RFC 6038 layout of the test packets: Symmetrical Size,
	// and the length of padding to reflect under Reflect Octets.
	Layout wire.PaddingLayout
}

// SessionStats are the packet counts of one session.
type SessionStats struct {
	// Received counts test packets that arrived for the session.
	Received uint64
	// Reflected counts replies sent.
	Reflected uint64
	// LastReceived is the arrival time of the last test packet, zero if none.
	LastReceived time.Time
}

// Session is the reflector side of one test session.
type Session interface {
	// Start begins reflecting packets that arrive from at (Start-Sessions).
	// A handler may hold them until the Start Time instead, which is on the
	// sender's clock.
	Start(at time.Time)
	// Stop keeps reflecting packets that arrive before until, and ignores
	// later ones.
	Stop(until time.Time)
	// Close releases the socket. The server calls it exactly once.
	Close() error
	// Stats returns the packet counts; the server reads LastReceived for
	// REFWAIT.
	Stats() SessionStats
}

// SessionHandler opens the reflector side of test sessions. internal/reflector
// implements it for the daemon; the server never creates sockets itself.
type SessionHandler interface {
	// Open binds the socket of a session at spec.Local. Return an error
	// matching ErrPortInUse (or syscall.EADDRINUSE / EACCES) when the port
	// cannot be bound, so that the server tries another one, and one matching
	// ErrAddressNotLocal (or syscall.EADDRNOTAVAIL) when the address is not
	// this host's, and ErrUnsupportedRequest when it cannot serve the session
	// at all. Other errors answer the request with Accept 2.
	Open(spec SessionSpec) (Session, error)
}

// Errors a SessionHandler returns from Open. The reflector returns the
// syscall errors these stand for; other handlers may return these.
//
//declscope:ignore overexported // part of the SessionHandler contract; the tests' fake handler returns them
var (
	// ErrPortInUse makes the server try another Receiver Port.
	ErrPortInUse = errors.New("port in use")
	// ErrAddressNotLocal answers the request with Accept 3: the Receiver
	// Address is not this host's.
	ErrAddressNotLocal = errors.New("address is not local")
	// ErrUnsupportedRequest refuses a session the reflector cannot serve, such
	// as packets longer than it accepts; the server answers Accept 3.
	ErrUnsupportedRequest = errors.New("session not supported by the reflector")
)

// SessionState is the state of a test session on the server (SPEC §5.7).
type SessionState uint8

// Test session states. A released session is no longer listed.
const (
	SessionAccepted SessionState = iota + 1 // SID issued, socket bound
	SessionActive                           // reflecting
	SessionDraining                         // stopped, reflecting until Timeout
)

// String returns the state name.
func (s SessionState) String() string {
	switch s {
	case SessionAccepted:
		return "accepted"
	case SessionActive:
		return "active"
	case SessionDraining:
		return "draining"
	default:
		return "unknown"
	}
}

// SessionInfo is a snapshot of one test session, as ListReflectorSessions
// reports it.
type SessionInfo struct {
	SID         wire.SID
	ControlPeer netip.AddrPort
	Sender      netip.AddrPort
	Reflector   netip.AddrPort
	State       SessionState
	Padding     uint32
	DSCP        uint8
	Timeout     time.Duration
	AcceptedAt  time.Time
	// Security is the mode of the control connection that requested it.
	Security wire.SecurityMode
	Stats    SessionStats
}
