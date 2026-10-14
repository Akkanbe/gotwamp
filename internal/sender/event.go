package sender

import (
	"net/netip"
	"time"

	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// PacketResult is one reply from the reflector.
type PacketResult struct {
	// Window is the index of the window the packet belongs to; 0 in a run
	// with a Count.
	Window int
	// Record holds the timestamps, sequence numbers and TTLs of the reply.
	// T1 is the send time the reflector echoed.
	Record stats.PacketRecord
	// Result classifies the reply and holds its per-packet metrics.
	Result stats.Result
	// SenderErrorEstimate is the Sender Error Estimate the reflector echoed.
	SenderErrorEstimate errest.Estimate
	// ReflectorErrorEstimate is the reflector's Error Estimate.
	ReflectorErrorEstimate errest.Estimate
	// TOS is the TOS / Traffic Class of the reply, or -1 when unavailable.
	TOS int
	// T1FromKernel reports that Record.T1, and so the metrics, use the
	// kernel transmit timestamp T1' instead of the user-space T1 written in
	// the packet (SPEC §8.2).
	T1FromKernel bool
}

// LostPacket is a packet that got no reply within the timeout.
type LostPacket struct {
	// Window is the index of the window the packet belongs to.
	Window int
	// Seq is the Sender Sequence Number.
	Seq uint32
	// T1 is the send time.
	T1 time.Time
}

// Summary is the result of a run with a Count, or of one window of a
// continuous session.
type Summary struct {
	stats.Summary
	// Window is the index of the window, from 0; 0 in a run with a Count.
	Window int
	// StartedAt is the scheduled send time of the first packet.
	StartedAt time.Time
	// EndedAt is when the last packet was decided, or when the run was
	// aborted.
	EndedAt time.Time
	// Aborted reports that the run or window was canceled before it
	// completed. Its undecided packets are counted in Pending, not in Lost.
	Aborted bool
	// SID identifies the TWAMP session of a Full run; it is zero for Light.
	SID wire.SID
	// Invalid reports that the results may be wrong: the TWAMP-Control
	// connection of a Full run was lost (SPEC §5.6, RunSummary.invalid), or
	// the run failed after the summary was made.
	Invalid bool
	// Malformed counts replies from the reflector that could not be decoded,
	// and those dropped for an Error Estimate Multiplier of 0 with
	// DropZeroMultiplier.
	Malformed uint64
	// ForeignSource counts datagrams from any address or port other than
	// the target.
	ForeignSource uint64
	// Local is the address and port the packets were sent from.
	Local netip.AddrPort
	// Remote is the target.
	Remote netip.AddrPort
	// Timestamping is the timestamping mode of the socket.
	Timestamping tstamp.Mode
	// TxTimestamps reports that kernel transmit timestamps (T1') were in
	// use.
	TxTimestamps bool
	// TxMissing counts accepted replies whose T1' did not arrive within
	// TxTimestampWait, which used the user-space T1
	// (tx_timestamp_missing_total, SPEC §8.2).
	TxMissing uint64
	// TxInvalid counts accepted replies whose T1' was more than 100 µs
	// before the user-space T1 or after T4, which used the user-space T1.
	TxInvalid uint64
	// ReflectedMismatch counts replies whose padding did not start with the
	// octets to be reflected (RFC 6038 Reflect Octets): a reflector that
	// does not keep the padding. They are measured all the same.
	ReflectedMismatch uint64
}
