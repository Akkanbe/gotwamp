// Package ntptime converts between time.Time and the 64-bit NTP timestamp
// format used by TWAMP (RFC 5905 §6, RFC 4656 §4.1.2).
//
// A Timestamp holds seconds since 0h 1 January 1900 UTC in its high 32 bits
// and the fraction of a second in units of 2^-32 s in its low 32 bits. The
// seconds field wraps on 7 February 2036 (the start of NTP era 1), so decoding
// needs a reference time to pick the era.
package ntptime

import (
	"math"
	"time"
)

// unixOffset is the number of seconds from the NTP prime epoch (1900-01-01)
// to the Unix epoch (1970-01-01), RFC 5905 JAN_1970.
const unixOffset = 2208988800

const (
	nanosPerSecond = 1_000_000_000
	fracHalf       = 1 << 31
)

// Timestamp is a 64-bit NTP timestamp: 32 bits of seconds since 1900 and 32
// bits of fraction. Encode it with encoding/binary in network byte order.
type Timestamp uint64

// New builds a Timestamp from its seconds and fraction fields.
func New(sec, frac uint32) Timestamp {
	return Timestamp(uint64(sec)<<32 | uint64(frac))
}

// FromTime converts t to an NTP timestamp. The seconds field is taken modulo
// 2^32, so times from 2036-02-07T06:28:16Z on land in era 1. The fraction is
// rounded to the nearest 2^-32 s, which keeps the conversion monotonic and
// lets Time recover the original nanosecond exactly.
func FromTime(t time.Time) Timestamp {
	sec := uint32((t.Unix() + unixOffset) & math.MaxUint32) // mod 2^32
	ns := uint64(t.Nanosecond() & math.MaxInt32)            // always below 1e9
	frac := (ns<<32 + nanosPerSecond/2) / nanosPerSecond    // at most 2^32 - 4
	return New(sec, uint32(frac&math.MaxUint32))
}

// FromDuration converts a non-negative interval to NTP format, as used by the
// Timeout field of Request-TW-Session. Negative durations encode as zero and
// intervals beyond 2^32 s saturate.
func FromDuration(d time.Duration) Timestamp {
	if d <= 0 {
		return 0
	}
	sec := uint64(d / nanosPerSecond)
	if sec > math.MaxUint32 {
		return math.MaxUint64
	}
	frac := (uint64(d%nanosPerSecond)<<32 + nanosPerSecond/2) / nanosPerSecond
	return New(uint32(sec), uint32(frac&math.MaxUint32))
}

// Seconds returns the seconds field.
func (t Timestamp) Seconds() uint32 { return uint32(t >> 32) }

// Fraction returns the fraction field in units of 2^-32 s.
func (t Timestamp) Fraction() uint32 { return uint32(t & math.MaxUint32) }

// IsZero reports whether every bit of t is zero. TWAMP uses an all-zero
// timestamp for "not set" (Start Time 0 means start immediately).
func (t Timestamp) IsZero() bool { return t == 0 }

// Time converts t to a time.Time in UTC. The era is chosen so that the result
// lies within [ref-2^31 s, ref+2^31 s), which keeps decoding continuous across
// the 2036 era rollover. Pass the local receive time as ref.
func (t Timestamp) Time(ref time.Time) time.Time {
	refSec := ref.Unix() + unixOffset
	delta := int64(t.Seconds() - uint32(refSec&math.MaxUint32))
	if delta >= 1<<31 {
		delta -= 1 << 32
	}
	sec := refSec + delta
	return time.Unix(sec-unixOffset, fracToNanos(t.Fraction())).UTC()
}

// Duration interprets t as an unsigned interval, as in the Timeout field of
// Request-TW-Session, rounded to the nearest nanosecond.
func (t Timestamp) Duration() time.Duration {
	return time.Duration(int64(t.Seconds())*nanosPerSecond + fracToNanos(t.Fraction()))
}

// Sub returns t-u rounded to the nearest nanosecond. The difference is taken
// modulo 2^64 and read as signed, so it is correct across an era rollover as
// long as the two timestamps are less than 2^31 s apart.
func (t Timestamp) Sub(u Timestamp) time.Duration {
	if t-u >= 1<<63 {
		return -(u - t).Duration()
	}
	return (t - u).Duration()
}

// fracToNanos converts a fraction in units of 2^-32 s to nanoseconds, rounding
// to nearest. The result may be 1e9 for fractions just below one second.
func fracToNanos(frac uint32) int64 {
	return int64((uint64(frac)*nanosPerSecond + fracHalf) >> 32)
}
