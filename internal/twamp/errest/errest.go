// Package errest implements the TWAMP Error Estimate (RFC 4656 §4.1.2, with
// the Z bit of RFC 8186 §2.3).
//
// The 16-bit field is S(1) | Z(1) | Scale(6) | Multiplier(8). The error it
// states is Multiplier × 2^(Scale−32) seconds; S is set when the clock is
// synchronized to UTC, and Z selects the timestamp format (0 NTP, 1 PTPv2).
package errest

import (
	"fmt"
	"math"
	"math/bits"
	"time"
)

const (
	sBit       = 0x8000
	zBit       = 0x4000
	scaleShift = 8
	scaleMask  = 0x3F
	maxScale   = scaleMask
	maxMult    = math.MaxUint8
	nanosPerS  = 1_000_000_000
)

// Unknown is the estimate sent when the error cannot be bounded: S and Z clear,
// Scale 63, Multiplier 255 (the largest representable error, as the reference
// implementation does).
const Unknown Estimate = maxScale<<scaleShift | maxMult

// Estimate is an encoded Error Estimate. Encode it with encoding/binary in
// network byte order.
type Estimate uint16

// New builds an Estimate from its fields. Only the low 6 bits of scale are used.
func New(synchronized, ptp bool, scale, multiplier uint8) Estimate {
	e := Estimate(scale&scaleMask)<<scaleShift | Estimate(multiplier)
	if synchronized {
		e |= sBit
	}
	if ptp {
		e |= zBit
	}
	return e
}

// Compute encodes the error bound maxErr as SPEC §8.4 describes: the smallest
// Scale whose Multiplier = ceil(maxErr / 2^(Scale−32)) fits in 8 bits. The
// result never understates maxErr and never carries Multiplier 0; a zero or
// negative maxErr encodes as the smallest error, 2^-32 s. Z is always 0 (NTP).
func Compute(maxErr time.Duration, synchronized bool) Estimate {
	ns := uint64(max(maxErr, 0))
	for scale := uint8(0); scale < maxScale; scale++ {
		if m, ok := multiplierAt(ns, scale); ok {
			return New(synchronized, false, scale, max(m, 1))
		}
	}
	// Scale 63 always fits: 255 × 2^31 s exceeds any time.Duration.
	m, _ := multiplierAt(ns, maxScale)
	return New(synchronized, false, maxScale, m)
}

// multiplierAt returns ceil(ns × 2^(32−scale) / 1e9) and whether it fits in a
// Multiplier.
func multiplierAt(ns uint64, scale uint8) (uint8, bool) {
	var m, rem uint64
	if scale >= 32 {
		div := uint64(nanosPerS) << (scale - 32) // at most 1e9 × 2^31, fits
		m, rem = ns/div, ns%div
	} else {
		k := 32 - scale
		hi, lo := ns>>(64-k), ns<<k
		if hi >= nanosPerS {
			return 0, false // quotient would not fit in 64 bits
		}
		m, rem = bits.Div64(hi, lo, nanosPerS)
	}
	if rem != 0 {
		m++
	}
	if m > maxMult {
		return 0, false
	}
	return uint8(m), true
}

// Synchronized reports the S bit: the sender's clock is synchronized to UTC.
func (e Estimate) Synchronized() bool { return e&sBit != 0 }

// PTP reports the Z bit (RFC 8186): the timestamps are in truncated PTPv2
// format rather than NTP.
func (e Estimate) PTP() bool { return e&zBit != 0 }

// Scale returns the 6-bit Scale field.
func (e Estimate) Scale() uint8 { return uint8(e>>scaleShift) & scaleMask }

// Multiplier returns the 8-bit Multiplier field.
func (e Estimate) Multiplier() uint8 { return uint8(e & maxMult) }

// Valid reports whether the Multiplier is non-zero. RFC 4656 §4.1.2 calls a
// packet with Multiplier 0 corrupt; SPEC §7.6 decides whether to keep it.
func (e Estimate) Valid() bool { return e.Multiplier() != 0 }

// Seconds returns the error in seconds. The value is exact in float64.
func (e Estimate) Seconds() float64 {
	return math.Ldexp(float64(e.Multiplier()), int(e.Scale())-32)
}

// Duration returns the error rounded up to whole nanoseconds, saturating at
// the largest time.Duration.
func (e Estimate) Duration() time.Duration {
	v := int64(e.Multiplier()) * nanosPerS
	scale := e.Scale()
	if scale < 32 {
		k := 32 - scale
		return time.Duration((v + 1<<k - 1) >> k)
	}
	k := scale - 32
	if v > math.MaxInt64>>k {
		return math.MaxInt64
	}
	return time.Duration(v << k)
}

// String formats the estimate for logs, for example "S Z=0 4.9e-05s".
func (e Estimate) String() string {
	s := "-"
	if e.Synchronized() {
		s = "S"
	}
	z := 0
	if e.PTP() {
		z = 1
	}
	return fmt.Sprintf("%s Z=%d %gs", s, z, e.Seconds())
}
