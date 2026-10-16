// Package reflector serves TWAMP Light and Full reflectors. ListenLight and
// ListenFull bind sockets; Serve runs until its context is canceled or Close
// is called. Close releases sockets and joins all workers, including Full
// control connections and test sessions. A service can be served only once.
//
// UDP timestamping requires Linux. Configuration defaults and validation are
// available on every platform and perform no network I/O.
package reflector

import (
	"log/slog"
	"time"

	engine "github.com/Akkanbe/gotwamp/internal/reflector"
)

// Deps provides clock quality and operational logging. Clock reports whether
// the clock is synchronized and its maximum error; nil means unknown. Logger
// defaults to discarding messages. Callbacks must be safe for concurrent use.
type Deps struct {
	Clock  func() (synchronized bool, maxErr time.Duration)
	Logger *slog.Logger
}

// Config configures a Light reflector. Call WithDefaults before Validate.
type (
	Config = engine.Config
	// FullConfig configures a Full reflector. Call WithDefaults before Validate.
	FullConfig = engine.FullConfig
	// Light is a bound Light reflector.
	Light = engine.Light
	// Full is a bound Full reflector, including its control server. A permanent
	// control accept error stops the service and is returned by Serve.
	Full = engine.FullService
	// Status is a snapshot of Light listeners, counters and flows.
	Status = engine.Status
	// ListenerStatus reports a Light listener's address, timestamping and counters.
	ListenerStatus = engine.ListenerStatus
	// FullStatus reports Full listeners, control counts and packet counters.
	FullStatus = engine.FullStatus
	// ControlStatus reports Full connection and session counts.
	ControlStatus = engine.ControlStatus
	// SessionInfo describes an active or draining Full session.
	SessionInfo = engine.SessionInfo
	// SessionState describes a Full session's lifecycle.
	SessionState = engine.SessionState
	// SessionStats reports a Full session's packet counts and last arrival.
	SessionStats = engine.SessionStats
	// PortRange is an inclusive range of UDP test ports.
	PortRange = engine.PortRange
	// Flow is a snapshot of a Light flow.
	Flow = engine.Flow
	// Counters reports received, reflected, failed and dropped packets.
	Counters = engine.Counters
	// DropReason identifies why a packet was discarded.
	DropReason = engine.DropReason
	// Sequence selects per-flow numbering or copying the sender's sequence.
	Sequence = engine.Sequence
	// DSCPPolicy selects copying or fixing the response DSCP.
	DSCPPolicy = engine.DSCPPolicy
	// VerifySender selects the source check for Full test packets.
	VerifySender = engine.VerifySender
)

const (
	// SequencePerFlow numbers replies independently for each flow.
	SequencePerFlow = engine.SequencePerFlow
	// SequenceCopySender copies the request sequence number.
	SequenceCopySender = engine.SequenceCopySender
	// DSCPCopy copies the incoming DSCP.
	DSCPCopy = engine.DSCPCopy
	// DSCPFixed uses Config.DSCPFixed.
	DSCPFixed = engine.DSCPFixed
	// VerifyAddress requires the negotiated sender address.
	VerifyAddress = engine.VerifyAddress
	// VerifyAddressPort also requires the negotiated sender port.
	VerifyAddressPort = engine.VerifyAddressPort
	// VerifyNone pins the first valid packet's source, supporting PAT.
	VerifyNone = engine.VerifyNone
	// SessionAccepted is bound and awaiting Start-Sessions.
	SessionAccepted = engine.SessionAccepted
	// SessionActive is reflecting test packets.
	SessionActive = engine.SessionActive
	// SessionDraining reflects until the stop timeout expires.
	SessionDraining = engine.SessionDraining
	// DropACL means a Light source failed its access policy.
	DropACL = engine.DropACL
	// DropMalformed means an invalid length, layout or authentication tag.
	DropMalformed = engine.DropMalformed
	// DropUnknownSession means a Full source failed verification.
	DropUnknownSession = engine.DropUnknownSession
	// DropRateLimit means a Light source exceeded its rate limit.
	DropRateLimit = engine.DropRateLimit
	// DropNotStarted means a Full session has not started.
	DropNotStarted = engine.DropNotStarted
	// DropDrained means a Full session's stop timeout elapsed.
	DropDrained = engine.DropDrained
)

// DropReasons lists all packet drop reasons in metric label order.
func DropReasons() []DropReason { return engine.DropReasons() }

// ListenLight applies defaults, validates cfg, and binds UDP listeners.
func ListenLight(cfg Config, deps Deps) (*Light, error) {
	return engine.ListenLight(cfg, engine.Deps{Clock: deps.Clock, Logger: deps.Logger})
}

// ListenFull applies defaults, validates cfg, and binds TCP control listeners.
// UDP test sockets are opened when control clients request sessions.
func ListenFull(cfg FullConfig, deps Deps) (*Full, error) {
	return engine.ListenFull(cfg, engine.Deps{Clock: deps.Clock, Logger: deps.Logger})
}
