package store

import (
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
)

// Defaults of SPEC Appendix A.
const (
	// defaultHistory is result_history: the summaries kept per session.
	defaultHistory = 1440
	// defaultMaxPackets is max_samples_per_run: the per-packet records kept
	// of the latest run.
	defaultMaxPackets = 100_000
	// defaultWatchBuffer is the event buffer of a Watcher.
	defaultWatchBuffer = 256
)

// Store is the query side of a result store. A persistent implementation
// can replace Memory behind it (SPEC §13).
type Store interface {
	ResultSink
	// Recent returns up to n of the latest summaries and run errors of
	// session, oldest first. n ≤ 0 returns all of them.
	Recent(session string, n int) []Event
	// Range returns the summaries and run errors of session whose Time is
	// in [from, to), oldest first.
	Range(session string, from, to time.Time) []Event
	// Packets returns the per-packet records of the latest run or window of
	// session, in arrival order.
	Packets(session string) []PacketSample
	// Sessions returns the names of the sessions with results, sorted.
	Sessions() []string
	// Delete forgets every result of session.
	Delete(session string)
	// Watch subscribes to new events.
	Watch(opts WatchOptions) *Watcher
}

// Memory is the in-memory Store. Its methods are safe for concurrent use.
// Summaries of ad hoc measurements (empty session name) are not kept, but
// watchers see them.
type Memory struct {
	history    int
	maxPackets int

	mu       sync.Mutex
	sessions map[string]*sessionResults
	watchers map[*Watcher]struct{}
}

// sessionResults is the history of one session.
type sessionResults struct {
	// ring holds the summaries and errors; next is where the next one goes.
	ring []Event
	next int
	full bool
	// packets holds the latest records of the run and window identified by
	// packetsRun and packetsWindow, as a ring once it reaches maxPackets.
	packets       []PacketSample
	packetsNext   int
	packetsRun    string
	packetsWindow int
}

// NewMemory returns a Memory keeping history summaries per session and
// maxPackets per-packet records of the latest run. Values ≤ 0 select
// 1440 and 100,000 (SPEC appendix A).
func NewMemory(history, maxPackets int) *Memory {
	if history <= 0 {
		history = defaultHistory
	}
	if maxPackets <= 0 {
		maxPackets = defaultMaxPackets
	}
	return &Memory{
		history:    history,
		maxPackets: maxPackets,
		sessions:   make(map[string]*sessionResults),
		watchers:   make(map[*Watcher]struct{}),
	}
}

// Publish stores e and passes it to the watchers. The Summary and Error
// it points to must not be modified afterwards.
func (m *Memory) Publish(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name := e.Session.Name; name != "" {
		switch e.Kind {
		case EventRunSummary, EventWindowSummary, EventRunError:
			m.results(name).add(e, m.history)
		case EventPacket:
			m.results(name).addPacket(e, m.maxPackets)
		}
	}
	for w := range m.watchers {
		w.offer(e)
	}
}

func (m *Memory) results(name string) *sessionResults {
	r := m.sessions[name]
	if r == nil {
		r = &sessionResults{}
		m.sessions[name] = r
	}
	return r
}

func (r *sessionResults) add(e Event, history int) {
	if len(r.ring) < history {
		r.ring = append(r.ring, e)
		return
	}
	r.ring[r.next] = e
	r.next = (r.next + 1) % history
	r.full = true
}

func (r *sessionResults) addPacket(e Event, limit int) {
	if e.RunID != r.packetsRun || e.Packet.Window != r.packetsWindow {
		r.packets, r.packetsNext = r.packets[:0], 0
		r.packetsRun, r.packetsWindow = e.RunID, e.Packet.Window
	}
	p := sampleOf(e.Packet)
	if len(r.packets) < limit {
		r.packets = append(r.packets, p)
		return
	}
	r.packets[r.packetsNext] = p
	r.packetsNext = (r.packetsNext + 1) % limit
}

// ordered returns the ring oldest first.
func (r *sessionResults) ordered() []Event {
	if !r.full {
		return slices.Clone(r.ring)
	}
	return append(slices.Clone(r.ring[r.next:]), r.ring[:r.next]...)
}

// PacketSample is the record the store keeps of one reply: 64 octets rather
// than the 280 of sender.PacketResult, so that max_samples_per_run records
// of a session stay within the 64 octets each of SPEC §9.4. Result turns it
// back into a sender.PacketResult.
type PacketSample struct {
	// T4 is the receive time in Unix nanoseconds.
	T4 int64
	// RTT, Forward, Reverse and ReflectorDelay are the delays; zero when
	// DelayUsable is clear.
	RTT, Forward, Reverse, ReflectorDelay time.Duration
	// Seq and ReflectorSeq are the sequence numbers.
	Seq, ReflectorSeq uint32
	// Window is the window of the packet.
	Window int32
	// Kind classifies the reply: a stats.Kind.
	Kind uint8
	// SenderTTL and ReplyTTL are the TTLs (0: unknown).
	SenderTTL, ReplyTTL uint8
	// Flags holds the packetOWDValid, packetReordered, packetT1FromKernel,
	// packetDelayUsable and packetDSCPECN bits; Result reads them.
	Flags uint8
	// DSCPECN is the S-DSCP-ECN octet of the reply (RFC 7750), valid with
	// packetDSCPECN.
	DSCPECN uint8
}

// Bits of PacketSample.Flags.
const (
	packetOWDValid uint8 = 1 << iota
	packetReordered
	packetT1FromKernel
	packetDelayUsable
	packetDSCPECN
)

func sampleOf(p sender.PacketResult) PacketSample {
	m := p.Result.Metrics
	s := PacketSample{
		T4: p.Record.T4.UnixNano(), RTT: m.RTT, Forward: m.Forward, Reverse: m.Reverse, ReflectorDelay: m.ReflectorDelay,
		Seq: p.Record.SenderSeq, ReflectorSeq: p.Record.ReflectorSeq, Window: int32(p.Window), //nolint:gosec // G115: windows of a run fit
		Kind:      uint8(p.Result.Kind), //nolint:gosec // G115: a handful of kinds
		SenderTTL: p.Record.SenderTTL, ReplyTTL: p.Record.ReplyTTL, DSCPECN: p.Record.DSCPECN,
	}
	for _, f := range []struct {
		on  bool
		bit uint8
	}{
		{m.OWDValid, packetOWDValid},
		{p.Result.Reordered, packetReordered},
		{p.T1FromKernel, packetT1FromKernel},
		{m.DelayUsable, packetDelayUsable},
		{p.Record.HasDSCPECN, packetDSCPECN},
	} {
		if f.on {
			s.Flags |= f.bit
		}
	}
	return s
}

// Result returns the sender.PacketResult the sample was made from, with
// what the sample keeps: the sequence numbers, T4, the TTLs, the DSCP and
// ECN, the delays and flags. T1–T3, the Error Estimates, the clock offset
// and the hop counts are not kept and come back zero.
func (p PacketSample) Result() sender.PacketResult {
	has := func(bit uint8) bool { return p.Flags&bit != 0 }
	return sender.PacketResult{
		Window: int(p.Window),
		Record: stats.PacketRecord{
			SenderSeq: p.Seq, ReflectorSeq: p.ReflectorSeq, T4: time.Unix(0, p.T4),
			SenderTTL: p.SenderTTL, ReplyTTL: p.ReplyTTL, DSCPECN: p.DSCPECN, HasDSCPECN: has(packetDSCPECN),
		},
		Result: stats.Result{
			Kind: stats.Kind(p.Kind),
			Metrics: stats.Metrics{
				RTT: p.RTT, Forward: p.Forward, Reverse: p.Reverse, ReflectorDelay: p.ReflectorDelay,
				OWDValid: has(packetOWDValid), DelayUsable: has(packetDelayUsable),
			},
			Reordered: has(packetReordered),
		},
		T1FromKernel: has(packetT1FromKernel),
	}
}

// Recent implements Store.
func (m *Memory) Recent(session string, n int) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.sessions[session]
	if r == nil {
		return nil
	}
	all := r.ordered()
	if n > 0 && n < len(all) {
		all = all[len(all)-n:]
	}
	return all
}

// Range implements Store.
func (m *Memory) Range(session string, from, to time.Time) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.sessions[session]
	if r == nil {
		return nil
	}
	var out []Event
	for _, e := range r.ordered() {
		if !e.Time.Before(from) && e.Time.Before(to) {
			out = append(out, e)
		}
	}
	return out
}

// Packets implements Store.
func (m *Memory) Packets(session string) []PacketSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.sessions[session]
	if r == nil {
		return nil
	}
	return append(slices.Clone(r.packets[r.packetsNext:]), r.packets[:r.packetsNext]...)
}

// Sessions implements Store.
func (m *Memory) Sessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.sessions))
	for name := range m.sessions {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Delete implements Store.
func (m *Memory) Delete(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, session)
}

// WatchOptions selects the events of a Watcher.
type WatchOptions struct {
	// Session limits the events to one session; empty means every session,
	// ad hoc measurements included.
	Session string
	// Packets adds the EventPacket and EventLost events.
	Packets bool
	// Buffer is the channel capacity; ≤ 0 means 256.
	Buffer int
}

// Watcher receives new events. Events that find its channel full are
// dropped and counted, so that a slow subscriber never holds up a run.
type Watcher struct {
	m       *Memory
	opts    WatchOptions
	c       chan Event
	dropped atomic.Uint64
	closed  bool
}

// Watch implements Store.
func (m *Memory) Watch(opts WatchOptions) *Watcher {
	if opts.Buffer <= 0 {
		opts.Buffer = defaultWatchBuffer
	}
	w := &Watcher{m: m, opts: opts, c: make(chan Event, opts.Buffer)}
	m.mu.Lock()
	m.watchers[w] = struct{}{}
	m.mu.Unlock()
	return w
}

// offer delivers e unless it is filtered out or the channel is full. m.mu
// is held.
func (w *Watcher) offer(e Event) {
	if w.opts.Session != "" && e.Session.Name != w.opts.Session {
		return
	}
	if !w.opts.Packets && (e.Kind == EventPacket || e.Kind == EventLost) {
		return
	}
	select {
	case w.c <- e:
	default:
		w.dropped.Add(1)
	}
}

// C returns the channel of events. It is closed by Close.
func (w *Watcher) C() <-chan Event {
	return w.c
}

// Dropped returns the number of events dropped because the channel was full.
func (w *Watcher) Dropped() uint64 {
	return w.dropped.Load()
}

// Close unsubscribes w and closes its channel. It may be called more than
// once.
func (w *Watcher) Close() {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	delete(w.m.watchers, w)
	close(w.c)
}
