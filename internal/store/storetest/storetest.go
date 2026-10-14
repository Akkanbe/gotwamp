// Package storetest is the contract every store.Store implementation meets:
// one table of cases that the tests of the in-memory store and of each
// persistent one run (SPEC §13). It is for tests only.
package storetest

import (
	"slices"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// Factory returns an empty store keeping history summaries per session and
// maxPackets records of the latest run, which the test may use until it
// ends: a store that needs closing closes itself with t.Cleanup.
type Factory func(t *testing.T, history, maxPackets int) store.Store

// T0 is the time of the first event of the cases.
var T0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// Sessions of the cases. The ad hoc one has no name.
var (
	Alpha = &store.SessionInfo{Name: "alpha", Target: "192.0.2.10", Mode: "light", Labels: map[string]string{"site": "tokyo"}}
	Beta  = &store.SessionInfo{Name: "beta", Target: "192.0.2.11", Mode: "full", ControlPort: 862}
	adHoc = &store.SessionInfo{Target: "192.0.2.12", Mode: "light"}
)

// SummaryAt returns the run summary of info i minutes after T0, with Sent
// = i to tell them apart.
func SummaryAt(info *store.SessionInfo, i int) store.Event {
	return store.Event{
		Kind: store.EventRunSummary, Time: T0.Add(time.Duration(i) * time.Minute), Session: info,
		RunID: "run", Summary: &sender.Summary{Summary: stats.Summary{Sent: uint64(i)}}, //nolint:gosec // G115: small test values
	}
}

// Sents returns the Sent of each summary of events, -1 for other events.
func Sents(events []store.Event) []int {
	var out []int
	for _, e := range events {
		if e.Summary == nil {
			out = append(out, -1)
			continue
		}
		out = append(out, int(e.Summary.Sent)) //nolint:gosec // G115: small test values
	}
	return out
}

func packetEvent(run string, window int, seq uint32) store.Event {
	return store.Event{
		Kind: store.EventPacket, Session: Alpha, RunID: run,
		Packet: sender.PacketResult{Window: window, Record: stats.PacketRecord{SenderSeq: seq}},
	}
}

func seqs(ps []store.PacketSample) []uint32 {
	var out []uint32
	for _, p := range ps {
		out = append(out, p.Seq)
	}
	return out
}

// Run runs every case of the contract on stores from newStore.
func Run(t *testing.T, newStore Factory) {
	t.Run("history", func(t *testing.T) { history(t, newStore) })
	t.Run("range", func(t *testing.T) { timeRange(t, newStore) })
	t.Run("packets", func(t *testing.T) { packets(t, newStore) })
	t.Run("watch", func(t *testing.T) { watch(t, newStore) })
	t.Run("delete", func(t *testing.T) { deleteSession(t, newStore) })
}

// history: Recent keeps the latest summaries and run errors per session,
// oldest first; skipped runs, packets and ad hoc results are not kept.
func history(t *testing.T, newStore Factory) {
	s := newStore(t, 3, 0)
	for i := range 5 {
		s.Publish(SummaryAt(Alpha, i))
	}
	s.Publish(store.Event{Kind: store.EventRunError, Time: T0, Session: Beta, RunID: "r", Error: &store.RunError{Stage: "connect", Message: "refused", Accept: 0}})
	s.Publish(SummaryAt(adHoc, 9))
	s.Publish(store.Event{Kind: store.EventRunSkipped, Time: T0, Session: Alpha})
	s.Publish(packetEvent("run", 0, 1))

	if got := Sents(s.Recent("alpha", 0)); !slices.Equal(got, []int{2, 3, 4}) {
		t.Errorf("Recent(all) = %v", got)
	}
	if got := Sents(s.Recent("alpha", 2)); !slices.Equal(got, []int{3, 4}) {
		t.Errorf("Recent(2) = %v", got)
	}
	if got := Sents(s.Recent("alpha", 10)); len(got) != 3 {
		t.Errorf("Recent(10) = %v", got)
	}
	if got := s.Recent("beta", 0); len(got) != 1 || *got[0].Error != (store.RunError{Stage: "connect", Message: "refused"}) || got[0].Kind != store.EventRunError {
		t.Errorf("beta = %+v", got)
	}
	if got := s.Recent("nobody", 0); len(got) != 0 {
		t.Errorf("unknown session = %v", got)
	}
	if got := s.Sessions(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("Sessions = %v (ad hoc results are not kept)", got)
	}
	e := s.Recent("alpha", 1)[0]
	if e.Session.Name != "alpha" || e.Session.Target != Alpha.Target || e.Session.Labels["site"] != "tokyo" || e.RunID != "run" || !e.Time.Equal(T0.Add(4*time.Minute)) {
		t.Errorf("event %+v, session %+v", e, e.Session)
	}
}

// timeRange: Range returns the summaries in [from, to), oldest first.
func timeRange(t *testing.T, newStore Factory) {
	s := newStore(t, 10, 0)
	for i := range 6 {
		s.Publish(SummaryAt(Alpha, i))
	}
	s.Publish(store.Event{Kind: store.EventWindowSummary, Time: T0.Add(150 * time.Second), Session: Alpha, RunID: "w", Summary: &sender.Summary{Window: 2}})
	got := Sents(s.Range("alpha", T0.Add(time.Minute), T0.Add(3*time.Minute)))
	if !slices.Equal(got, []int{1, 2, 0}) {
		t.Errorf("Range = %v, want 1, 2 and the window summary at 2:30", got)
	}
	if got := s.Range("alpha", T0.Add(time.Hour), T0.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("empty range = %v", got)
	}
	if got := s.Range("beta", T0, T0.Add(time.Hour)); len(got) != 0 {
		t.Errorf("unknown session = %v", got)
	}
}

// packets: the records of the latest run or window, the latest maxPackets.
func packets(t *testing.T, newStore Factory) {
	s := newStore(t, 0, 3)
	for i := range 2 {
		s.Publish(packetEvent("r1", 0, uint32(i))) //nolint:gosec // G115: small test values
	}
	if got := seqs(s.Packets("alpha")); !slices.Equal(got, []uint32{0, 1}) {
		t.Errorf("Packets = %v", got)
	}
	for i := 2; i < 7; i++ {
		s.Publish(packetEvent("r1", 0, uint32(i))) //nolint:gosec // G115: small test values
	}
	if got := seqs(s.Packets("alpha")); !slices.Equal(got, []uint32{4, 5, 6}) {
		t.Errorf("Packets = %v", got)
	}
	s.Publish(packetEvent("r1", 1, 10))
	if got := seqs(s.Packets("alpha")); !slices.Equal(got, []uint32{10}) {
		t.Errorf("new window: %v", got)
	}
	s.Publish(packetEvent("r2", 1, 0))
	if got := seqs(s.Packets("alpha")); !slices.Equal(got, []uint32{0}) {
		t.Errorf("new run: %v", got)
	}
}

// watch: watchers see new events by their filters, and drop what does not
// fit their buffer.
func watch(t *testing.T, newStore Factory) {
	s := newStore(t, 0, 0)
	all := s.Watch(store.WatchOptions{Packets: true, Buffer: 10})
	onlyAlpha := s.Watch(store.WatchOptions{Session: "alpha"})
	tiny := s.Watch(store.WatchOptions{Buffer: 1})
	defer onlyAlpha.Close()
	defer tiny.Close()

	s.Publish(SummaryAt(Alpha, 1))
	s.Publish(packetEvent("r", 0, 1))
	s.Publish(store.Event{Kind: store.EventLost, Session: Alpha})
	s.Publish(SummaryAt(Beta, 2))
	s.Publish(SummaryAt(adHoc, 3))

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
	s.Publish(SummaryAt(Alpha, 4))
	if len(onlyAlpha.C()) != 1 {
		t.Error("other watchers stopped receiving")
	}
}

// deleteSession: Delete forgets every result of the session.
func deleteSession(t *testing.T, newStore Factory) {
	s := newStore(t, 0, 0)
	s.Publish(SummaryAt(Alpha, 1))
	s.Publish(SummaryAt(Beta, 1))
	s.Publish(packetEvent("r", 0, 1))
	s.Delete("alpha")
	if s.Recent("alpha", 0) != nil || s.Range("alpha", T0, T0.Add(time.Hour)) != nil || s.Packets("alpha") != nil {
		t.Error("alpha still has results after Delete")
	}
	if got := s.Sessions(); !slices.Equal(got, []string{"beta"}) {
		t.Errorf("Sessions = %v", got)
	}
	s.Publish(SummaryAt(Alpha, 2))
	if got := Sents(s.Recent("alpha", 0)); !slices.Equal(got, []int{2}) {
		t.Errorf("after Delete and a new summary: %v", got)
	}
}
