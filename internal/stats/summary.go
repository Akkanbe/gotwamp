package stats

import "time"

// Summary is the result of one run or one window (SPEC §7.2–§7.5, §9.4).
// Runs and windows share it; the caller records which one it is.
type Summary struct {
	// Sent is the number of packets sent, excluding send errors.
	Sent uint64
	// SendErrors is the number of packets that failed to send.
	SendErrors uint64
	// Received is the number of distinct Sender Sequence Numbers accepted.
	Received uint64
	// Lost is the number of packets with no reply by T1 + timeout.
	Lost uint64
	// Pending is the number of packets neither received nor timed out yet.
	// In a summary of an aborted run these are the aborted packets, which
	// count as neither received nor lost.
	Pending uint64
	// LossRatio is Lost / (Sent − Pending), or zero when nothing is decided.
	LossRatio float64
	// DupForward counts extra replies with a known Sender Sequence Number and
	// a new Reflector Sequence Number: copies made on the forward path.
	DupForward uint64
	// DupReverse counts extra replies repeating both sequence numbers: copies
	// made on the reverse path.
	DupReverse uint64
	// Reordered counts replies whose Sender Sequence Number was below NextExp
	// on arrival (RFC 4737 §3.3).
	Reordered uint64
	// ReorderedReverse applies the same test to the Reflector Sequence
	// Number, isolating the reverse path. It is zero for a stateless
	// reflector and for one whose sequence rewound during the run.
	ReorderedReverse uint64
	// LostForward is Sent − (R_max + 1) (RFC 8762 §4). Zero unless
	// LossSplitValid.
	LostForward uint64
	// LostReverse is (R_max + 1) − Received. Zero unless LossSplitValid.
	LostReverse uint64
	// LossSplitValid reports whether LostForward and LostReverse are
	// meaningful: the reflector is stateful, its sequence did not rewind,
	// nothing is pending, and neither estimate is negative. It is also false
	// when the reflector echoed every Sender Sequence Number unchanged and
	// packets were lost, since a stateless (copy_sender) reflector cannot be
	// told from a stateful one then.
	LossSplitValid bool
	// Late counts replies that arrived after T1 + timeout. Their packets are
	// counted as lost.
	Late uint64
	// Stray counts replies whose Sender Sequence Number matched no
	// outstanding packet.
	Stray uint64

	// RTT is the distribution of (T4 − T1) − (T3 − T2).
	RTT Distribution
	// Forward is the distribution of T2 − T1 over every usable packet,
	// whether OWDValid or not.
	Forward Distribution
	// Reverse is the distribution of T4 − T3, like Forward.
	Reverse Distribution
	// ReflectorDelay is the distribution of T3 − T2.
	ReflectorDelay Distribution
	// OWDValid reports whether every packet with usable timestamps had valid
	// one-way delays (SPEC §7.4), and there was at least one.
	OWDValid bool
	// NegativeOWD counts packets with a negative forward or reverse delay.
	NegativeOWD uint64
	// InvalidRTT counts accepted packets with a negative RTT, a sign of a
	// clock step during the run (SPEC §8.1). None of their delays are used.
	InvalidRTT uint64
	// Unsupported counts accepted packets whose timestamps were in a format
	// the sender could not convert, PTPv2 with the Z bit in v0.x
	// (unsupported_timestamp_format, SPEC §7.6). None of their delays are
	// used.
	Unsupported uint64
	// SenderSynced reports whether every accepted packet carried the sender's
	// S bit, and there was at least one.
	SenderSynced bool
	// ReflectorSynced is SenderSynced for the reflector's S bit.
	ReflectorSynced bool
	// MaxClockError is the largest per-packet sum of the Error Estimates.
	MaxClockError time.Duration
	// MaxClockErrorUnknown reports that some accepted packet carried an
	// Error Estimate with Multiplier 0 (SPEC §7.6): its error is unknown,
	// and MaxClockError does not bound it.
	MaxClockErrorUnknown bool
	// DSCPReflected summarizes the DSCP and ECN with which the test packets
	// reached the reflector (RFC 7750).
	DSCPReflected DSCPReflected
	// Jitter holds the delay variation metrics.
	Jitter Jitter
	// ForwardHops summarizes the send TTL minus the Sender TTL field.
	ForwardHops Hops
	// ReverseHops summarizes 255 minus the TTL of the replies.
	ReverseHops Hops
}

// DSCPReflected summarizes the S-DSCP-ECN octets of the replies (RFC 7750).
// When Count is zero the reflector reported none and the other fields are
// zero.
type DSCPReflected struct {
	// Count is the number of accepted replies that carried the octet.
	Count uint64
	// Mode is the most frequent DSCP, the smallest one on a tie.
	Mode uint8
	// Remarked counts packets whose DSCP the forward path changed from the
	// one sent (Config.SendDSCP).
	Remarked uint64
	// CE counts packets marked Congestion Experienced (ECN 11) on the way.
	CE uint64
}

// Jitter holds the delay variation metrics of SPEC §7.3.
type Jitter struct {
	// IPDVRTT is the distribution of |rtt(i) − rtt(i−1)| over pairs of
	// consecutive Sender Sequence Numbers that were both received (RFC 3393,
	// RFC 5481 §4.1). A lost packet leaves both of its pairs undefined.
	IPDVRTT Distribution
	// IPDVForward is IPDVRTT for the forward delay. It needs no clock
	// synchronization, since the offset cancels.
	IPDVForward Distribution
	// IPDVReverse is IPDVRTT for the reverse delay.
	IPDVReverse Distribution
	// PDV is the distribution of rtt − min(rtt) (RFC 5481 §4.2).
	PDV Distribution
	// RFC3550 is the smoothed jitter J += (|D| − J) / 16 of RFC 3550 §6.4.1,
	// with D the RTT difference of successive accepted replies in arrival
	// order, as it stood at the last one.
	RFC3550 time.Duration
	// P95P50 is the RTT 95th percentile minus its median, the jitter of
	// perfSONAR twping.
	P95P50 time.Duration
}
