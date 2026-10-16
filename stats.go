package gotwamp

import "time"

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
	// one configured for transmission.
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

// Distribution summarizes one delay metric over a run or window (SPEC §7.5).
// When Count is zero every other field is zero and undefined.
type Distribution struct {
	// Count is the number of values observed.
	Count uint64
	// Samples is the number of values the percentiles were computed from.
	// It is less than Count when the series was decimated.
	Samples int
	// Min is the smallest value observed.
	Min time.Duration
	// Max is the largest value observed.
	Max time.Duration
	// Mean is the arithmetic mean of all values observed.
	Mean time.Duration
	// StdDev is the sample standard deviation of all values observed. It is
	// zero when Count is one.
	StdDev time.Duration
	// Median is the 50th percentile.
	Median time.Duration
	// P90 is the 90th percentile.
	P90 time.Duration
	// P95 is the 95th percentile.
	P95 time.Duration
	// P99 is the 99th percentile.
	P99 time.Duration
	// P999 is the 99.9th percentile.
	P999 time.Duration
}

// Hops summarizes the hop counts of one direction over a run or window.
// When Count is zero the other fields are zero and undefined.
type Hops struct {
	// Count is the number of packets with a known hop count.
	Count uint64
	// Min is the smallest hop count observed.
	Min int
	// Max is the largest hop count observed.
	Max int
	// Mode is the most frequent hop count, the smallest one on a tie.
	Mode int
}

// Kind classifies a reply received during a measurement.
type Kind int

const (
	// KindAccepted is the first reply for its Sender Sequence Number.
	KindAccepted Kind = iota
	// KindDupForward is an extra reply with a new Reflector Sequence Number.
	KindDupForward
	// KindDupReverse is an extra reply repeating both sequence numbers.
	KindDupReverse
	// KindLate arrived after T1 + timeout. Its packet is counted as lost.
	KindLate
	// KindStray matched no outstanding packet.
	KindStray
)

// String returns the snake_case name of k.
func (k Kind) String() string {
	switch k {
	case KindAccepted:
		return "accepted"
	case KindDupForward:
		return "duplicate_forward"
	case KindDupReverse:
		return "duplicate_reverse"
	case KindLate:
		return "late"
	case KindStray:
		return "stray"
	default:
		return "unknown"
	}
}

// Result describes one reply received during a measurement.
type Result struct {
	// Kind classifies the reply. Only KindAccepted contributes delays.
	Kind Kind
	// Metrics are the reply's per-packet indicators, whatever its Kind.
	Metrics Metrics
	// Reordered reports whether an accepted reply arrived after a reply with
	// a larger Sender Sequence Number (RFC 4737 §3.3).
	Reordered bool
}
