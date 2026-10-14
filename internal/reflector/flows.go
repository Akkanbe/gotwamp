//declscope:namespace light

package reflector

import (
	"net/netip"
	"sync"
	"time"
)

// Flow is a snapshot of one Light flow (SPEC §6.6), as ListReflectorSessions
// reports it.
type Flow struct {
	// Source is the Session-Sender address and port.
	Source netip.AddrPort
	// Destination is the address and port the packets arrived at: the
	// listener's, or for a wildcard listener the address the socket reported.
	Destination netip.AddrPort
	// NextSeq is the Reflector Sequence Number of the next reply.
	NextSeq uint32
	// Received counts packets accepted into the flow.
	Received uint64
	// Reflected counts replies sent for the flow.
	Reflected uint64
	// FirstReceived and LastReceived are the first and last arrival times.
	FirstReceived time.Time
	LastReceived  time.Time
}

// flowKey identifies a flow by its 4-tuple.
type flowKey struct {
	src, dst netip.AddrPort
}

// flowTable holds the per-flow state of the Light reflector. A flow idle for
// longer than timeout is forgotten: the next packet starts it again from
// sequence 0. Once max flows exist, packets of new flows get no state.
type flowTable struct {
	mu       sync.Mutex
	flows    map[flowKey]*Flow
	timeout  time.Duration
	max      int
	overflow uint64
}

// newFlowTable returns an empty table.
func newFlowTable(timeout time.Duration, maxFlows int) *flowTable {
	return &flowTable{flows: make(map[flowKey]*Flow), timeout: timeout, max: maxFlows}
}

// receive records a packet of the flow (src, dst) arriving at now and returns
// the Reflector Sequence Number to use and whether the flow has state. A
// stateless packet (the table is full) returns senderSeq. With copySender the
// sequence number is always senderSeq, but the flow is still counted.
func (t *flowTable) receive(src, dst netip.AddrPort, now time.Time, senderSeq uint32, copySender bool) (uint32, bool) {
	k := flowKey{src, dst}
	t.mu.Lock()
	defer t.mu.Unlock()
	f, ok := t.flows[k]
	if ok && now.Sub(f.LastReceived) > t.timeout {
		delete(t.flows, k)
		ok = false
	}
	if !ok {
		if len(t.flows) >= t.max {
			t.overflow++
			return senderSeq, false
		}
		f = &Flow{Source: src, Destination: dst, FirstReceived: now}
		t.flows[k] = f
	}
	f.LastReceived = now
	f.Received++
	if copySender {
		f.NextSeq = senderSeq + 1
		return senderSeq, true
	}
	seq := f.NextSeq
	f.NextSeq++
	return seq, true
}

// reflected counts a reply sent for the flow (src, dst).
func (t *flowTable) reflected(src, dst netip.AddrPort) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if f, ok := t.flows[flowKey{src, dst}]; ok {
		f.Reflected++
	}
}

// expire forgets the flows idle for longer than the timeout at now.
func (t *flowTable) expire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, f := range t.flows {
		if now.Sub(f.LastReceived) > t.timeout {
			delete(t.flows, k)
		}
	}
}

// list returns a snapshot of the flows not yet expired at now, unordered.
func (t *flowTable) list(now time.Time) []Flow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Flow, 0, len(t.flows))
	for _, f := range t.flows {
		if now.Sub(f.LastReceived) <= t.timeout {
			out = append(out, *f)
		}
	}
	return out
}

// stats returns the number of flows with state and the overflow count.
func (t *flowTable) stats() (active int, overflow uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.flows), t.overflow
}
