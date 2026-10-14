package client

import (
	"errors"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Stage is the step of a run in which the control protocol failed. The
// values follow the RunStage enum of the gRPC API.
type Stage uint8

// Stages of a TWAMP-Control exchange.
//
//declscope:ignore overexported // the RunStage values of the gRPC API, all of which the tests produce
const (
	StageConnect  Stage = 1 // establishing the TCP connection
	StageGreeting Stage = 2 // waiting for or validating the Server Greeting
	StageSetup    Stage = 3 // Set-Up-Response and Server-Start
	StageRequest  Stage = 4 // Request-TW-Session and Accept-Session
	StageStart    Stage = 5 // Start-Sessions and Start-Ack
	StageTest     Stage = 6 // test packets flowing
	StageStop     Stage = 7 // Stop-Sessions
)

var stageNames = [...]string{
	StageConnect:  "connect",
	StageGreeting: "greeting",
	StageSetup:    "setup",
	StageRequest:  "request",
	StageStart:    "start",
	StageTest:     "test",
	StageStop:     "stop",
}

// String returns the stage name used in error messages.
func (s Stage) String() string {
	if int(s) < len(stageNames) && stageNames[s] != "" {
		return stageNames[s]
	}
	return fmt.Sprintf("Stage(%d)", uint8(s))
}

// Reasons a control exchange fails, wrapped in an *Error.
//
//declscope:ignore overexported // sentinels for errors.Is, each one matched by the tests
var (
	// ErrServerRefused means the Server Greeting offered no mode (Modes 0).
	ErrServerRefused = errors.New("server refused the connection (Modes 0)")
	// ErrNoCommonMode means the server offers none of the security modes the
	// client accepts.
	ErrNoCommonMode = errors.New("no common mode")
	// ErrCountTooLarge means the greeting's PBKDF2 count exceeds the limit.
	ErrCountTooLarge = errors.New("greeting count too large")
	// ErrCountTooSmall means the greeting's PBKDF2 count is below the 1024
	// RFC 4656 §3.1 requires, which an authenticated mode refuses.
	ErrCountTooSmall = errors.New("greeting count too small")
	// ErrRejected means the server answered with a non-zero Accept.
	ErrRejected = errors.New("rejected by server")
	// ErrAlternatePort means the server offered another port and the
	// configuration refuses alternate ports.
	ErrAlternatePort = errors.New("server offered an alternate port")
	// ErrControlLost means the control connection failed while sessions ran.
	ErrControlLost = errors.New("control connection lost")
	// ErrState means a method was called in a state that does not allow it.
	ErrState = errors.New("invalid control state")
	// ErrInvalidPort means the server accepted a session on port 0.
	ErrInvalidPort = errors.New("server accepted the session on port 0")
	// ErrReflectedOctets means the server did not return the Octets to be
	// reflected in Accept-Session (RFC 6038 §4.3).
	ErrReflectedOctets = errors.New("server did not reflect the octets to be reflected")
	// ErrUnexpectedMessage means the server sent a message that does not
	// answer the one sent, such as an acknowledgement of other sessions.
	ErrUnexpectedMessage = errors.New("unexpected message from the server")
	// ErrTimeout means the server did not answer within the control timeout.
	ErrTimeout = errors.New("control timeout")
)

// Error is a failure of the control protocol.
type Error struct {
	// Stage is where the exchange failed.
	Stage Stage
	// Accept is the server's non-zero Accept value, when Err is ErrRejected.
	Accept wire.Accept
	// Err is the cause.
	Err error
}

// Error formats the failure, for example "control timeout in greeting
// (after 15s)" or
// "request: rejected by server: accept-session accept=5 (temporary resource limitation)".
func (e *Error) Error() string {
	if errors.Is(e.Err, ErrTimeout) {
		return e.Err.Error() // "control timeout in <stage> ...", SPEC §5.6
	}
	return e.Stage.String() + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }
