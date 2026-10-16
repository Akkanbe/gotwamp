// Package stats computes TWAMP measurement statistics from timestamped
// packet records: RTT, one-way delays, IPDV, PDV, loss, duplication and
// reordering (SPEC §7).
//
// The package reads no clock. The caller reports each send, each reply and
// the passage of time to an [Accumulator], and reads a [Summary] at the end
// of a run or window.
package stats

import (
	"math"
	"time"

	"github.com/Akkanbe/gotwamp"
)

const (
	// DefaultTimeout is the default reply timeout (SPEC Appendix A).
	DefaultTimeout = 2 * time.Second
	// defaultMaxSamples is the default max_samples_per_run (SPEC Appendix A).
	defaultMaxSamples = 100_000
)

// Config parameterizes an [Accumulator]. The zero value selects the defaults.
type Config struct {
	// Timeout is how long after T1 a reply is accepted. Zero means
	// DefaultTimeout.
	Timeout time.Duration
	// MaxSamples bounds the values each delay metric keeps for percentiles.
	// Zero means 100,000. Beyond it the values are decimated at
	// equal intervals.
	//
	// Seven metrics keep samples (RTT, forward, reverse, reflector delay and
	// three IPDV series), so the samples take at most 7 × 8 × MaxSamples
	// bytes. The per-packet state for loss and duplicate detection is held
	// only until T1 + Timeout, so it is bounded by the send rate times
	// Timeout.
	MaxSamples int
	// SendTTL is the IP TTL the sender packets carry. Zero means 255.
	SendTTL uint8
	// StatelessReflector is set when the reflector copies the Sender Sequence
	// Number instead of counting its own (RFC 5357 Appendix I). The
	// direction of loss and of reordering is then not estimated, and every
	// duplicate is classified as reverse.
	StatelessReflector bool
	// SendDSCP is the DSCP the sender packets carry, against which the DSCP
	// the reflector saw is compared (RFC 7750).
	SendDSCP uint8
}

// Kind is the canonical public value.
type Kind = gotwamp.Kind

// Result is the canonical public value.
type Result = gotwamp.Result

// KindAccepted is the canonical public constant.
const KindAccepted = gotwamp.KindAccepted

// KindDupForward is the canonical public constant.
const KindDupForward = gotwamp.KindDupForward

// KindDupReverse is the canonical public constant.
const KindDupReverse = gotwamp.KindDupReverse

// KindLate is the canonical public constant.
const KindLate = gotwamp.KindLate

// KindStray is the canonical public constant.
const KindStray = gotwamp.KindStray

// Accumulator turns a stream of sends and replies into a [Summary]. Events
// must be reported in time order: every reply with T4 ≤ now must be added
// before Expire(now). It is not safe for concurrent use.
type Accumulator struct {
	cfg Config

	// queue holds every packet sent within the timeout, in send order.
	// index maps a Sender Sequence Number to its position, counted from the
	// first packet ever sent; queue[0] is position base.
	queue []outstanding
	base  int
	index map[uint32]int

	sent, sendErrors, received, lost    uint64
	dupForward, dupReverse, late, stray uint64
	reordered, reorderedReverse         uint64
	negativeOWD, invalidRTT, pending    uint64
	unsupported                         uint64
	senderUnsynced, reflectorUnsynced   uint64
	owdInvalid                          uint64

	// nextExp and nextExpReverse are RFC 4737's NextExp of both sequence
	// numbers, compared in serial number arithmetic (before) so that a
	// sequence wrapping past 2^32 is not taken for reordering.
	nextExp           uint32
	nextExpSet        bool
	nextExpReverse    uint32
	nextExpReverseSet bool

	rMax    uint32
	rMaxT3  time.Time
	rMaxSet bool
	rewound bool
	// rMin is the smallest Reflector Sequence Number seen, and knownBase
	// the base given by SetReflectorBase: see reflectorBase.
	rMin         uint32
	rMinSet      bool
	knownBase    uint32
	knownBaseSet bool
	// seqDiffer is set once a reply carries a Reflector Sequence Number
	// other than its Sender Sequence Number.
	seqDiffer bool

	rtt, forward, reverse, reflectorDelay *series
	ipdvRTT, ipdvForward, ipdvReverse     *series
	maxClockError                         time.Duration
	clockErrorUnknown                     bool
	// dscps counts the reflected DSCPs; remarked and ce as in DSCPReflected.
	dscps                    [64]uint64
	dscpCount                uint64
	remarked, ce             uint64
	jitter                   float64
	prevRTT                  time.Duration
	prevRTTSet               bool
	forwardHops, reverseHops hopCounter
	retiredLast              outstanding
	retiredLastSet           bool
}

// outstanding is the state of one sent packet until T1 + timeout.
type outstanding struct {
	seq          uint32
	t1           time.Time
	deadline     time.Time
	received     bool
	late         bool
	unsent       bool
	reflectorSeq uint32
	// copies holds the Reflector Sequence Numbers of the forward-path
	// duplicates seen, so that a reverse-path copy of one of them is not
	// taken for another forward duplicate. It grows only on duplicates.
	copies   []uint32
	hasDelay bool
	rtt      time.Duration
	forward  time.Duration
	reverse  time.Duration
}

// NewAccumulator returns an empty Accumulator.
func NewAccumulator(cfg Config) *Accumulator {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxSamples <= 0 {
		cfg.MaxSamples = defaultMaxSamples
	}
	n := cfg.MaxSamples
	return &Accumulator{
		cfg:            cfg,
		index:          make(map[uint32]int),
		rtt:            newSeries(n),
		forward:        newSeries(n),
		reverse:        newSeries(n),
		reflectorDelay: newSeries(n),
		ipdvRTT:        newSeries(n),
		ipdvForward:    newSeries(n),
		ipdvReverse:    newSeries(n),
	}
}

// Sent records that the packet seq was sent at t1. It reports false, and
// records nothing, when seq is still outstanding.
func (a *Accumulator) Sent(seq uint32, t1 time.Time) bool {
	if _, ok := a.index[seq]; ok {
		return false
	}
	if !a.nextExpSet {
		a.nextExp = seq
		a.nextExpSet = true
	}
	a.index[seq] = a.base + len(a.queue)
	a.queue = append(a.queue, outstanding{seq: seq, t1: t1, deadline: t1.Add(a.cfg.Timeout)})
	a.sent++
	a.pending++
	return true
}

// Unsent turns a packet recorded by Sent into a send error, for a sender
// that records each packet before handing it to the socket. It reports
// false, and changes nothing, unless seq is outstanding with no reply yet.
// A later reply for seq is stray.
func (a *Accumulator) Unsent(seq uint32) bool {
	pos, ok := a.index[seq]
	if !ok {
		return false
	}
	o := &a.queue[pos-a.base]
	if o.received || o.late {
		return false
	}
	o.unsent = true
	delete(a.index, seq)
	a.sent--
	a.pending--
	a.sendErrors++
	return true
}

// Resent moves the send time of outstanding packet seq to t1, for a sender
// that sent it again with a fresh timestamp. It reports false, and changes
// nothing, unless seq is outstanding with no reply yet.
func (a *Accumulator) Resent(seq uint32, t1 time.Time) bool {
	pos, ok := a.index[seq]
	if !ok {
		return false
	}
	o := &a.queue[pos-a.base]
	if o.received || o.late {
		return false
	}
	o.t1, o.deadline = t1, t1.Add(a.cfg.Timeout)
	return true
}

// Pending returns the number of packets neither received nor timed out, as
// Summary().Pending does, without building a summary.
func (a *Accumulator) Pending() uint64 {
	return a.pending
}

// Add records a reply and classifies it. Delays are taken only from the
// first reply of each Sender Sequence Number (RFC 7679 §3.5).
func (a *Accumulator) Add(r PacketRecord) Result {
	res := Result{Metrics: measure(r, a.cfg.SendTTL)}
	pos, ok := a.index[r.SenderSeq]
	if !ok {
		a.stray++
		res.Kind = KindStray
		return res
	}
	o := &a.queue[pos-a.base]
	if r.T4.After(o.deadline) {
		a.late++
		if !o.received && !o.late {
			o.late = true
			a.lost++
			a.pending--
		}
		res.Kind = KindLate
		return res
	}
	if o.late {
		res.Kind = KindLate
		a.late++
		return res
	}
	if o.received {
		if o.seenReflectorSeq(r.ReflectorSeq) {
			a.dupReverse++
			res.Kind = KindDupReverse
		} else {
			a.dupForward++
			res.Kind = KindDupForward
			o.copies = append(o.copies, r.ReflectorSeq)
		}
		return res
	}

	o.received = true
	o.reflectorSeq = r.ReflectorSeq
	a.received++
	a.pending--
	res.Kind = KindAccepted
	res.Reordered = a.order(r)
	a.observe(o, r, res.Metrics)
	return res
}

// seenReflectorSeq reports whether a reply with Reflector Sequence Number
// rseq was already received for o: the first copy, or a forward-path
// duplicate.
func (o *outstanding) seenReflectorSeq(rseq uint32) bool {
	if rseq == o.reflectorSeq {
		return true
	}
	for _, c := range o.copies {
		if c == rseq {
			return true
		}
	}
	return false
}

// order applies RFC 4737 §3.3 to both sequence numbers of a first reply and
// reports whether the Sender Sequence Number was reordered.
func (a *Accumulator) order(r PacketRecord) bool {
	reordered := before(r.SenderSeq, a.nextExp)
	if reordered {
		a.reordered++
	} else {
		a.nextExp = r.SenderSeq + 1
	}
	if r.ReflectorSeq != r.SenderSeq {
		a.seqDiffer = true
	}
	if a.cfg.StatelessReflector {
		return reordered
	}
	if a.nextExpReverseSet && before(r.ReflectorSeq, a.nextExpReverse) {
		a.reorderedReverse++
	} else {
		a.nextExpReverse, a.nextExpReverseSet = r.ReflectorSeq+1, true
	}
	if !a.rMinSet || r.ReflectorSeq < a.rMin {
		a.rMin, a.rMinSet = r.ReflectorSeq, true
	}
	// A stateful reflector numbers reflections in the order it sends them,
	// and never gives one number to two packets. A smaller or equal number
	// sent later means its counter restarted (SPEC §6.6).
	// T3 is compared only when both timestamps are usable.
	usable := !r.ReflectorClock.UnsupportedFormat
	switch {
	case !a.rMaxSet || r.ReflectorSeq > a.rMax:
		a.rMax, a.rMaxSet = r.ReflectorSeq, true
		a.rMaxT3 = time.Time{}
		if usable {
			a.rMaxT3 = r.T3
		}
	case r.ReflectorSeq == a.rMax:
		a.rewound = true
	case usable && !a.rMaxT3.IsZero() && r.T3.After(a.rMaxT3):
		a.rewound = true
	}
	return reordered
}

// before reports whether sequence number a comes before b in serial
// number arithmetic (RFC 1982): within 2^31 behind it, across a wrap too.
func before(a, b uint32) bool {
	return int32(a-b) < 0 //nolint:gosec // G115: the difference is meant to wrap
}

// observe folds the indicators of a first reply into the run.
func (a *Accumulator) observe(o *outstanding, r PacketRecord, m Metrics) {
	if !r.SenderClock.Synced {
		a.senderUnsynced++
	}
	if !r.ReflectorClock.Synced {
		a.reflectorUnsynced++
	}
	a.maxClockError = max(a.maxClockError, m.MaxClockError)
	a.clockErrorUnknown = a.clockErrorUnknown || m.ClockErrorUnknown
	if r.HasDSCPECN {
		dscp, ecn := r.DSCPECN>>2, r.DSCPECN&3
		a.dscps[dscp]++
		a.dscpCount++
		if dscp != a.cfg.SendDSCP {
			a.remarked++
		}
		if ecn == 3 {
			a.ce++
		}
	}
	a.forwardHops.add(m.ForwardHops)
	a.reverseHops.add(m.ReverseHops)
	if !m.DelayUsable {
		a.unsupported++ // a timestamp format the caller could not convert (SPEC §7.6)
		return
	}
	// A negative RTT means a clock step during the run (SPEC §8.1).
	if m.RTT < 0 {
		a.invalidRTT++
		return
	}
	o.hasDelay, o.rtt, o.forward, o.reverse = true, m.RTT, m.Forward, m.Reverse
	a.rtt.add(m.RTT)
	a.forward.add(m.Forward)
	a.reverse.add(m.Reverse)
	a.reflectorDelay.add(m.ReflectorDelay)
	if !m.OWDValid {
		a.owdInvalid++
	}
	if m.Forward < 0 || m.Reverse < 0 {
		a.negativeOWD++
	}
	if a.prevRTTSet {
		d := float64(abs(m.RTT - a.prevRTT))
		a.jitter += (d - a.jitter) / 16
	}
	a.prevRTT, a.prevRTTSet = m.RTT, true
}

// Lost identifies a packet that Expire found lost.
type Lost struct {
	// Seq is the Sender Sequence Number.
	Seq uint32
	// T1 is the send time given to Sent.
	T1 time.Time
}

// Expire retires every packet whose deadline T1 + timeout is not after now,
// in send order. A packet retired without a reply is lost, and a later
// reply for it is stray. The lost packets are appended to lost, which is
// returned, including those already counted lost when a late reply came:
// every lost packet is reported once, in send order.
func (a *Accumulator) Expire(now time.Time, lost []Lost) []Lost {
	i := 0
	for ; i < len(a.queue) && !a.queue[i].deadline.After(now); i++ {
		o := a.queue[i]
		if !o.received && !o.unsent {
			if !o.late {
				a.lost++
				a.pending--
			}
			lost = append(lost, Lost{Seq: o.seq, T1: o.t1})
		}
		a.retiredLast, a.retiredLastSet = ipdv(a.retiredLast, a.retiredLastSet, o,
			a.ipdvRTT, a.ipdvForward, a.ipdvReverse)
		delete(a.index, o.seq)
	}
	if i == 0 {
		return lost
	}
	a.base += i
	a.queue = append(a.queue[:0], a.queue[i:]...)
	return lost
}

// ipdv adds the variation between prev and cur to the three series when
// they are consecutive in sequence and both have delays, and returns cur as
// the next prev.
func ipdv(prev outstanding, prevSet bool, cur outstanding, rtt, forward, reverse *series) (outstanding, bool) {
	if prevSet && prev.hasDelay && cur.hasDelay && cur.seq == prev.seq+1 {
		rtt.add(abs(cur.rtt - prev.rtt))
		forward.add(abs(cur.forward - prev.forward))
		reverse.add(abs(cur.reverse - prev.reverse))
	}
	return cur, true
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// Summary returns the statistics so far. It does not change the
// Accumulator: packets still outstanding are reported as Pending, and
// replies already received for them count toward IPDV.
func (a *Accumulator) Summary() Summary {
	s := Summary{
		Sent:                 a.sent,
		SendErrors:           a.sendErrors,
		Received:             a.received,
		Lost:                 a.lost,
		DupForward:           a.dupForward,
		DupReverse:           a.dupReverse,
		Reordered:            a.reordered,
		Late:                 a.late,
		Stray:                a.stray,
		RTT:                  a.rtt.distribution(),
		Forward:              a.forward.distribution(),
		Reverse:              a.reverse.distribution(),
		ReflectorDelay:       a.reflectorDelay.distribution(),
		OWDValid:             a.rtt.n > 0 && a.owdInvalid == 0,
		NegativeOWD:          a.negativeOWD,
		InvalidRTT:           a.invalidRTT,
		Unsupported:          a.unsupported,
		Pending:              a.pending,
		SenderSynced:         a.received > 0 && a.senderUnsynced == 0,
		ReflectorSynced:      a.received > 0 && a.reflectorUnsynced == 0,
		MaxClockError:        a.maxClockError,
		MaxClockErrorUnknown: a.clockErrorUnknown,
		DSCPReflected:        a.dscpReflected(),
		ForwardHops:          a.forwardHops.hops(),
		ReverseHops:          a.reverseHops.hops(),
	}
	if decided := s.Sent - s.Pending; decided > 0 {
		s.LossRatio = float64(s.Lost) / float64(decided)
	}
	if !a.cfg.StatelessReflector && !a.rewound {
		s.ReorderedReverse = a.reorderedReverse
		a.splitLoss(&s)
	}

	rtt, forward, reverse := a.ipdvRTT.clone(), a.ipdvForward.clone(), a.ipdvReverse.clone()
	prev, prevSet := a.retiredLast, a.retiredLastSet
	for _, o := range a.queue {
		prev, prevSet = ipdv(prev, prevSet, o, rtt, forward, reverse)
	}
	s.Jitter = Jitter{
		IPDVRTT:     rtt.distribution(),
		IPDVForward: forward.distribution(),
		IPDVReverse: reverse.distribution(),
		PDV:         shift(s.RTT, s.RTT.Min),
		RFC3550:     time.Duration(math.Round(a.jitter)),
	}
	if s.RTT.Count > 0 {
		s.Jitter.P95P50 = s.RTT.P95 - s.RTT.Median
	}
	return s
}

// reflectorBase returns the Reflector Sequence Number of the first
// reflection of this run or window, and whether it is known. A packet lost
// forward takes no number and one lost back leaves a gap, so the direction
// of loss rests on the base; and with any loss the replies cannot rule out
// a lost packet that overtook the others on the way out and took a lower
// number (SPEC §7.2). So the base is never inferred from replies when
// packets were lost:
//
//   - SetReflectorBase gives it: 0 for a new Full session, or the end of
//     the previous window when that end was certain (NextReflectorBase);
//   - with no loss every reflection was seen, and the base is the smallest
//     number.
func (a *Accumulator) reflectorBase() (uint32, bool) {
	switch {
	case a.knownBaseSet:
		return a.knownBase, true
	case a.lost == 0 && a.pending == 0 && a.rMinSet:
		return a.rMin, true
	}
	return 0, false
}

// SetReflectorBase gives the Reflector Sequence Number of the first
// reflection of this run or window, when the session tells it: 0 for a new
// Full session, NextReflectorBase of the previous window otherwise.
func (a *Accumulator) SetReflectorBase(base uint32) {
	a.knownBase, a.knownBaseSet = base, true
}

// NextReflectorBase returns the number the reflector gives the first
// reflection after this run or window: R_max + 1. It is certain only when
// every reflection of the window was seen: a known base, no packet lost or
// pending, and numbers without a gap from the base to R_max, forward
// duplicates included. A lost packet could have been reflected after
// R_max, its reply lost too, and taken the next number. It is never known
// for a stateless reflector or a counter that rewound.
func (a *Accumulator) NextReflectorBase() (uint32, bool) {
	if !a.rMaxSet || a.lost != 0 || a.pending != 0 || a.rewound || a.cfg.StatelessReflector {
		return 0, false
	}
	base, ok := a.reflectorBase()
	if !ok || a.rMin < base || uint64(a.rMax-base)+1 != a.received+a.dupForward {
		return 0, false
	}
	return a.rMax + 1, true
}

func (a *Accumulator) dscpReflected() DSCPReflected {
	d := DSCPReflected{Count: a.dscpCount, Remarked: a.remarked, CE: a.ce}
	var best uint64
	for v, n := range a.dscps {
		if n > best {
			best, d.Mode = n, uint8(v) //nolint:gosec // G115: v < 64
		}
	}
	return d
}

// splitLoss fills the direction of loss estimated from R_max (RFC 8762 §4)
// for a stateful reflector whose sequence did not rewind.
func (a *Accumulator) splitLoss(s *Summary) {
	if !a.rMaxSet || s.Pending > 0 {
		return
	}
	// A reflector that copies the Sender Sequence Number (copy_sender) and
	// a stateful one that lost nothing forward both echo R = S. With loss,
	// the two would split it differently, and nothing tells them apart.
	if !a.seqDiffer && s.Lost > 0 {
		return
	}
	base, ok := a.reflectorBase()
	// A number below the base means a packet of the previous window was
	// overtaken across the boundary, and the windows cannot be told apart.
	if !ok || a.rMin < base {
		return
	}
	// rMax ≥ rMin ≥ base. Every forward duplicate seen took a number of
	// its own. Reflections lost back after R_max at the end are counted as
	// forward loss, as RFC 8762 §4 does at the end of a stream; such a
	// window does not pass its end on (NextReflectorBase).
	numbers := uint64(a.rMax-base) + 1
	if numbers < a.dupForward {
		return
	}
	reflected := numbers - a.dupForward
	if reflected > s.Sent || reflected < s.Received {
		return
	}
	s.LostForward = s.Sent - reflected
	s.LostReverse = reflected - s.Received
	s.LossSplitValid = true
}
