// Package errest implements the TWAMP Error Estimate (RFC 4656 §4.1.2, with
// the Z bit of RFC 8186 §2.3).
//
// The 16-bit field is S(1) | Z(1) | Scale(6) | Multiplier(8). The error it
// states is Multiplier × 2^(Scale−32) seconds; S is set when the clock is
// synchronized to UTC, and Z selects the timestamp format (0 NTP, 1 PTPv2).
package errest

import (
	"time"

	"github.com/Akkanbe/gotwamp"
)

// Estimate is the public error estimate.
type Estimate = gotwamp.ErrorEstimate

// Unknown is the largest representable error estimate.
const Unknown = gotwamp.UnknownErrorEstimate

// New builds an estimate from its fields.
func New(synchronized, ptp bool, scale, multiplier uint8) Estimate {
	return gotwamp.NewErrorEstimate(synchronized, ptp, scale, multiplier)
}

// Compute encodes the error bound without understating it.
func Compute(maxErr time.Duration, synchronized bool) Estimate {
	return gotwamp.ComputeErrorEstimate(maxErr, synchronized)
}
