// Package store keeps the results of the monitoring sessions (SPEC §9.4):
// a ring buffer of run and window summaries per session, the per-packet
// records of the latest run, and subscriptions to new results.
//
// Results travel as [Event] values through [ResultSink] implementations.
// The scheduler publishes every event to one sink, usually a [Fanout] of
// the store and the exporters (SPEC §13).
package store

import (
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
)

// EventKind identifies what an Event carries.
type EventKind uint8

const (
	// EventRunSummary carries the Summary of a periodic or ad hoc run.
	EventRunSummary EventKind = iota + 1
	// EventWindowSummary carries the Summary of one window of a continuous
	// session.
	EventWindowSummary
	// EventRunError carries the Error of a run that failed.
	EventRunError
	// EventRunSkipped reports a periodic run not started because the
	// previous one was still running.
	EventRunSkipped
	// EventPacket carries the Packet result of one reply.
	EventPacket
	// EventLost carries a Lost packet.
	EventLost
)

// String returns the record type name of k, as JSON Lines writes it.
func (k EventKind) String() string {
	switch k {
	case EventRunSummary:
		return "run_summary"
	case EventWindowSummary:
		return "window_summary"
	case EventRunError:
		return "run_error"
	case EventRunSkipped:
		return "run_skipped"
	case EventPacket:
		return "packet"
	case EventLost:
		return "lost"
	default:
		return "unknown"
	}
}

// SessionInfo describes the session an event belongs to. One value is shared
// by every event of a session and must not be modified.
type SessionInfo struct {
	// Name is the session name; empty for an ad hoc measurement.
	Name string
	// Target is the target as configured: a host name or an address.
	Target string
	// Mode is "full" or "light".
	Mode string
	// Labels are the user-defined Prometheus labels.
	Labels map[string]string
	// Count is the packet count of a periodic run; 0 for continuous.
	Count uint32
	// Interval is the packet interval.
	Interval time.Duration
	// Padding is the padding length in octets.
	Padding int
	// DSCP is the DSCP of the test packets.
	DSCP int
	// ControlPort is the TWAMP-Control port of a Full session.
	ControlPort uint16
	// Definition is the session's full definition as its owner keeps it,
	// opaque to the store and the exporters. A redefined session gets a new
	// SessionInfo, so the results of each run keep the definition they were
	// measured with.
	Definition any
}

// RunError describes a failed run (SPEC §9.5 RunError).
type RunError struct {
	// Stage is where the run failed: connect, greeting, setup, request,
	// start, test or stop.
	Stage string
	// Message is the error text.
	Message string
	// Accept is the Accept value the server answered, or 0.
	Accept int
}

// Event is one result of a session. Kind selects the field that is set.
type Event struct {
	// Kind identifies the payload.
	Kind EventKind
	// Time is when the event happened: the end of a run or window, the
	// receive time of a packet, or the time of a failure.
	Time time.Time
	// Session describes the session. It is never nil.
	Session *SessionInfo
	// RunID identifies the run (a ULID).
	RunID string
	// SID is the TWAMP session identifier of a Full run, when known.
	SID []byte
	// Summary is set for EventRunSummary and EventWindowSummary.
	Summary *sender.Summary
	// Error is set for EventRunError.
	Error *RunError
	// Packet is set for EventPacket.
	Packet sender.PacketResult
	// Lost is set for EventLost.
	Lost sender.LostPacket
}

// ResultSink receives events. Publish must not block: a sink that cannot
// keep up drops events and counts them.
type ResultSink interface {
	Publish(e Event)
}

// Fanout publishes every event to each of its sinks, in order.
type Fanout []ResultSink

// Publish implements ResultSink.
func (f Fanout) Publish(e Event) {
	for _, s := range f {
		s.Publish(e)
	}
}
