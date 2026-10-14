package reflector

import "sync/atomic"

// DropReason is why the reflector discarded a test packet. The values follow
// the DropReason enum of the gRPC API and the reason label of
// gotwamp_reflector_packets_dropped_total (SPEC §9.6).
type DropReason uint8

// Drop reasons, in the order of the gRPC enum.
//
//declscope:ignore overexported // daemon reaches them through DropReasons; the tests name each one
const (
	DropACL            DropReason = iota // source not allowed by the ACL (Light)
	DropMalformed                        // too short or too long, or failed its HMAC (Full)
	DropUnknownSession                   // source does not match the Full session
	DropRateLimit                        // per-source rate limit exceeded (Light)
	DropNotStarted                       // Full session not started yet
	DropDrained                          // after Stop-Sessions plus Timeout (Full)
	numDropReasons
)

var dropReasonNames = [numDropReasons]string{
	DropACL:            "acl",
	DropMalformed:      "malformed",
	DropUnknownSession: "unknown_session",
	DropRateLimit:      "rate_limit",
	DropNotStarted:     "not_started",
	DropDrained:        "drained",
}

// String returns the metric label of r.
func (r DropReason) String() string {
	if r < numDropReasons {
		return dropReasonNames[r]
	}
	return "unknown"
}

// DropReasons lists every reason, in label order for metrics.
func DropReasons() []DropReason {
	return []DropReason{DropACL, DropMalformed, DropUnknownSession, DropRateLimit, DropNotStarted, DropDrained}
}

// Counters is a snapshot of packet counters.
type Counters struct {
	// Received counts every datagram read, including dropped ones.
	Received uint64
	// Reflected counts replies sent.
	Reflected uint64
	// SendErrors counts replies that could not be sent, after the one retry.
	SendErrors uint64
	// Dropped counts discarded packets, indexed by DropReason.
	Dropped [numDropReasons]uint64
}

// add accumulates o into c.
//
//declscope:package // Light.Status sums its listeners
func (c *Counters) add(o Counters) {
	c.Received += o.Received
	c.Reflected += o.Reflected
	c.SendErrors += o.SendErrors
	for i := range c.Dropped {
		c.Dropped[i] += o.Dropped[i]
	}
}

// counters is the live, atomically updated form of Counters.
//
//declscope:package // updated by every listener of light.go
type counters struct {
	received   atomic.Uint64
	reflected  atomic.Uint64
	sendErrors atomic.Uint64
	dropped    [numDropReasons]atomic.Uint64
}

// snapshot reads the counters. Each field is read atomically; the set is not.
//
//declscope:package // read by Light.Status
func (c *counters) snapshot() Counters {
	s := Counters{
		Received:   c.received.Load(),
		Reflected:  c.reflected.Load(),
		SendErrors: c.sendErrors.Load(),
	}
	for i := range c.dropped {
		s.Dropped[i] = c.dropped[i].Load()
	}
	return s
}
