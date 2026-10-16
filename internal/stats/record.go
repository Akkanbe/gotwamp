package stats

import (
	"math"
	"time"

	"github.com/Akkanbe/gotwamp"
)

// ClockStatus is the canonical public value.
type ClockStatus = gotwamp.ClockStatus

// PacketRecord is the canonical public value.
type PacketRecord = gotwamp.PacketRecord

// Metrics is the canonical public value.
type Metrics = gotwamp.Metrics

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
