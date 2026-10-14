package stats

import (
	"math"
	"time"
)

// ClockStatus is one end's clock state as carried by its Error Estimate
// (RFC 4656 §4.1.2, SPEC §7.6).
type ClockStatus struct {
	// Synced is the S bit: the end's clock is synchronized to an external source.
	Synced bool
	// Error is the Error Estimate converted to a duration.
	Error time.Duration
	// ErrorUnknown is set when the Error Estimate Multiplier was zero. The
	// packet is still accepted, but its one-way delays are invalid.
	ErrorUnknown bool
	// UnsupportedFormat is set when the Z bit selects a timestamp format the
	// caller could not convert (PTPv2 in v0.x). The packet counts toward loss,
	// duplication and reordering, but none of its delays are used.
	UnsupportedFormat bool
}

// PacketRecord is one reflected test packet as seen by the Session-Sender.
// Timestamps follow SPEC §3: T1 sender transmit, T2 reflector receive,
// T3 reflector transmit and T4 sender receive.
type PacketRecord struct {
	// SenderSeq is the Sender Sequence Number copied by the reflector.
	SenderSeq uint32
	// ReflectorSeq is the reflector's own Sequence Number.
	ReflectorSeq uint32
	// T1 is the sender transmit time: the kernel TX timestamp when one was
	// obtained, the user-space time written into the packet otherwise.
	T1 time.Time
	// T2 is the Receive Timestamp of the reflector packet.
	T2 time.Time
	// T3 is the Timestamp of the reflector packet.
	T3 time.Time
	// T4 is the sender receive time.
	T4 time.Time
	// SenderClock is decoded from the Sender Error Estimate.
	SenderClock ClockStatus
	// ReflectorClock is decoded from the reflector's Error Estimate.
	ReflectorClock ClockStatus
	// SenderTTL is the Sender TTL field of the reflector packet (offset 40).
	SenderTTL uint8
	// ReplyTTL is the TTL or Hop Limit of the received reflector packet.
	// Zero means it was not available.
	ReplyTTL uint8
	// DSCPECN is the S-DSCP-ECN octet of the reply (RFC 7750): the DSCP and
	// ECN with which the test packet reached the reflector. It is valid
	// only with HasDSCPECN.
	DSCPECN uint8
	// HasDSCPECN is set when the reflector reported DSCPECN.
	HasDSCPECN bool
}

// Metrics are the per-packet indicators of SPEC §7.1.
type Metrics struct {
	// RTT is (T4 − T1) − (T3 − T2).
	RTT time.Duration
	// RTTRaw is T4 − T1, including the reflector's processing time.
	RTTRaw time.Duration
	// Forward is the forward one-way delay T2 − T1.
	Forward time.Duration
	// Reverse is the reverse one-way delay T4 − T3.
	Reverse time.Duration
	// ReflectorDelay is T3 − T2.
	ReflectorDelay time.Duration
	// ClockOffset is ((T2 − T1) − (T4 − T3)) / 2: the clock offset plus half
	// the path asymmetry. It is a hint only.
	ClockOffset time.Duration
	// MaxClockError is the sum of both Error Estimates, the error bound of the
	// one-way delays. An unknown estimate contributes nothing, and sets
	// ClockErrorUnknown.
	MaxClockError time.Duration
	// ClockErrorUnknown reports that an end sent an Error Estimate with
	// Multiplier 0, so that MaxClockError understates the bound.
	ClockErrorUnknown bool
	// DelayUsable is false when a timestamp format was unsupported. All delay
	// fields are then zero.
	DelayUsable bool
	// OWDValid reports whether Forward and Reverse are meaningful (SPEC §7.4):
	// both clocks synchronized with a known error, and neither value negative.
	OWDValid bool
	// ForwardHops is the send TTL minus SenderTTL, or -1 when SenderTTL
	// exceeds the send TTL.
	ForwardHops int
	// ReverseHops is 255 minus ReplyTTL, or -1 when ReplyTTL is unknown.
	ReverseHops int
}

// saturatingAdd returns a + b for non-negative durations, or the largest
// Duration where the sum overflows: an unknown error estimate is the
// largest one (Scale 63, Multiplier 255), and two of them must not wrap.
func saturatingAdd(a, b time.Duration) time.Duration {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// defaultSendTTL is the default IP TTL of sender packets.
const defaultSendTTL = 255

// measure computes the per-packet indicators of r for packets sent with the
// IP TTL sendTTL. A sendTTL of zero means 255.
//
//declscope:package // the Accumulator measures every reply with it
func measure(r PacketRecord, sendTTL uint8) Metrics {
	if sendTTL == 0 {
		sendTTL = defaultSendTTL
	}
	m := Metrics{ForwardHops: -1, ReverseHops: -1}
	if r.SenderTTL <= sendTTL {
		m.ForwardHops = int(sendTTL) - int(r.SenderTTL)
	}
	if r.ReplyTTL != 0 {
		m.ReverseHops = 255 - int(r.ReplyTTL)
	}
	if !r.SenderClock.ErrorUnknown {
		m.MaxClockError = saturatingAdd(m.MaxClockError, r.SenderClock.Error)
	}
	if !r.ReflectorClock.ErrorUnknown {
		m.MaxClockError = saturatingAdd(m.MaxClockError, r.ReflectorClock.Error)
	}
	m.ClockErrorUnknown = r.SenderClock.ErrorUnknown || r.ReflectorClock.ErrorUnknown
	if r.SenderClock.UnsupportedFormat || r.ReflectorClock.UnsupportedFormat {
		return m
	}
	m.DelayUsable = true
	m.RTTRaw = r.T4.Sub(r.T1)
	m.ReflectorDelay = r.T3.Sub(r.T2)
	m.RTT = m.RTTRaw - m.ReflectorDelay
	m.Forward = r.T2.Sub(r.T1)
	m.Reverse = r.T4.Sub(r.T3)
	m.ClockOffset = (m.Forward - m.Reverse) / 2
	m.OWDValid = r.SenderClock.Synced && r.ReflectorClock.Synced &&
		!r.SenderClock.ErrorUnknown && !r.ReflectorClock.ErrorUnknown &&
		m.Forward >= 0 && m.Reverse >= 0
	return m
}
