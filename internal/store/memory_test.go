package store

import (
	"slices"
	"testing"
	"time"
	"unsafe"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
)

var (
	t0    = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	alpha = &SessionInfo{Name: "alpha", Target: "192.0.2.10", Mode: "light"}
	beta  = &SessionInfo{Name: "beta", Target: "192.0.2.11", Mode: "full"}
	adhoc = &SessionInfo{Target: "192.0.2.12", Mode: "light"}
)

func summaryAt(info *SessionInfo, i int) Event {
	return Event{
		Kind: EventRunSummary, Time: t0.Add(time.Duration(i) * time.Minute), Session: info,
		RunID: "run", Summary: &sender.Summary{Summary: stats.Summary{Sent: uint64(i)}},
	}
}

func sents(events []Event) []uint64 {
	var out []uint64
	for _, e := range events {
		out = append(out, e.Summary.Sent)
	}
	return out
}

func TestHistory(t *testing.T) {
	m := NewMemory(3, 0)
	for i := range 5 {
		m.Publish(summaryAt(alpha, i))
	}
	m.Publish(Event{Kind: EventRunError, Time: t0, Session: beta, Error: &RunError{Stage: "connect"}})
	m.Publish(summaryAt(adhoc, 9))
	m.Publish(Event{Kind: EventRunSkipped, Time: t0, Session: alpha})

	if got := sents(m.Recent("alpha", 0)); !slices.Equal(got, []uint64{2, 3, 4}) {
		t.Errorf("Recent(all) = %v", got)
	}
	if got := sents(m.Recent("alpha", 2)); !slices.Equal(got, []uint64{3, 4}) {
		t.Errorf("Recent(2) = %v", got)
	}
	if got := sents(m.Recent("alpha", 10)); len(got) != 3 {
		t.Errorf("Recent(10) = %v", got)
	}
	if got := sents(m.Range("alpha", t0.Add(3*time.Minute), t0.Add(4*time.Minute))); !slices.Equal(got, []uint64{3}) {
		t.Errorf("Range = %v", got)
	}
	if got := m.Recent("beta", 0); len(got) != 1 || got[0].Error.Stage != "connect" {
		t.Errorf("beta = %+v", got)
	}
	if got := m.Sessions(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("Sessions = %v (ad hoc results are not kept)", got)
	}
	m.Delete("alpha")
	if m.Recent("alpha", 0) != nil || m.Range("alpha", t0, t0.Add(time.Hour)) != nil || m.Packets("alpha") != nil {
		t.Error("alpha still has results after Delete")
	}

	small := NewMemory(0, 0)
	if small.history != defaultHistory || small.maxPackets != defaultMaxPackets {
		t.Errorf("defaults %d %d", small.history, small.maxPackets)
	}
}

func packetEvent(run string, window int, seq uint32) Event {
	return Event{
		Kind: EventPacket, Session: alpha, RunID: run,
		Packet: sender.PacketResult{Window: window, Record: stats.PacketRecord{SenderSeq: seq}},
	}
}

func seqs(ps []PacketSample) []uint32 {
	var out []uint32
	for _, p := range ps {
		out = append(out, p.Seq)
	}
	return out
}

func TestPacketSample(t *testing.T) {
	if size := unsafe.Sizeof(PacketSample{}); size > 64 {
		t.Errorf("PacketSample is %d octets, more than SPEC §9.4 plans", size)
	}
	p := sender.PacketResult{
		Window: 2,
		Record: stats.PacketRecord{
			SenderSeq: 7, ReflectorSeq: 6, T4: t0, SenderTTL: 250, ReplyTTL: 60, DSCPECN: 46<<2 | 1, HasDSCPECN: true,
		},
		Result: stats.Result{
			Kind: stats.KindDupForward, Reordered: true,
			Metrics: stats.Metrics{RTT: time.Millisecond, Forward: 2, Reverse: 3, ReflectorDelay: 4, OWDValid: true, DelayUsable: true},
		},
		T1FromKernel: true,
	}
	want := PacketSample{
		T4: t0.UnixNano(), RTT: time.Millisecond, Forward: 2, Reverse: 3, ReflectorDelay: 4,
		Seq: 7, ReflectorSeq: 6, Window: 2, Kind: uint8(stats.KindDupForward), SenderTTL: 250, ReplyTTL: 60,
		Flags:   packetOWDValid | packetReordered | packetT1FromKernel | packetDelayUsable | packetDSCPECN,
		DSCPECN: 46<<2 | 1,
	}
	if got := sampleOf(p); got != want {
		t.Errorf("sampleOf = %+v", got)
	}
	// Back to a PacketResult: everything the sample keeps survives.
	back := want.Result()
	if !back.Record.T4.Equal(t0) {
		t.Errorf("T4 %v", back.Record.T4)
	}
	back.Record.T4 = p.Record.T4
	if back != p {
		t.Errorf("Result =\n %+v\nwant\n %+v", back, p)
	}
}

func TestPackets(t *testing.T) {
	m := NewMemory(0, 3)
	for i := range 2 {
		m.Publish(packetEvent("r1", 0, uint32(i)))
	}
	if got := seqs(m.Packets("alpha")); !slices.Equal(got, []uint32{0, 1}) {
		t.Errorf("Packets = %v", got)
	}
	// Beyond the limit the latest are kept, in arrival order.
	for i := 2; i < 7; i++ {
		m.Publish(packetEvent("r1", 0, uint32(i)))
	}
	if got := seqs(m.Packets("alpha")); !slices.Equal(got, []uint32{4, 5, 6}) {
		t.Errorf("Packets = %v", got)
	}
	// A new window, and a new run, start afresh.
	m.Publish(packetEvent("r1", 1, 10))
	if got := seqs(m.Packets("alpha")); !slices.Equal(got, []uint32{10}) {
		t.Errorf("new window: %v", got)
	}
	m.Publish(packetEvent("r2", 1, 0))
	if got := seqs(m.Packets("alpha")); !slices.Equal(got, []uint32{0}) {
		t.Errorf("new run: %v", got)
	}
}

func TestWatch(t *testing.T) {
	m := NewMemory(0, 0)
	all := m.Watch(WatchOptions{Packets: true, Buffer: 10})
	onlyAlpha := m.Watch(WatchOptions{Session: "alpha"})
	tiny := m.Watch(WatchOptions{Buffer: 1})

	m.Publish(summaryAt(alpha, 1))
	m.Publish(packetEvent("r", 0, 1))
	m.Publish(Event{Kind: EventLost, Session: alpha})
	m.Publish(summaryAt(beta, 2))
	m.Publish(summaryAt(adhoc, 3))

	if n := len(all.C()); n != 5 {
		t.Errorf("all got %d events", n)
	}
	if n := len(onlyAlpha.C()); n != 1 || (<-onlyAlpha.C()).Summary.Sent != 1 {
		t.Errorf("alpha got %d events", n)
	}
	if tiny.Dropped() != 2 || all.Dropped() != 0 {
		t.Errorf("dropped %d / %d", tiny.Dropped(), all.Dropped())
	}

	all.Close()
	all.Close()
	for range all.C() {
	}
	m.Publish(summaryAt(alpha, 4))
	if len(onlyAlpha.C()) != 1 {
		t.Error("other watchers stopped receiving")
	}
	if onlyAlpha.opts.Buffer != defaultWatchBuffer {
		t.Errorf("default buffer %d", onlyAlpha.opts.Buffer)
	}
}
