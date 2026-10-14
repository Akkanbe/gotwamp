package stats

import (
	"math"
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

var synced = ClockStatus{Synced: true, Error: 10 * time.Microsecond}

// reply builds a record for a packet sent at sendMs that spent fwd on the
// forward path, refl in the reflector and rev on the reverse path.
func reply(seq, rseq uint32, sendMs, fwd, refl, rev float64) PacketRecord {
	t1 := t0.Add(ms(sendMs))
	t2 := t1.Add(ms(fwd))
	t3 := t2.Add(ms(refl))
	return PacketRecord{
		SenderSeq:      seq,
		ReflectorSeq:   rseq,
		T1:             t1,
		T2:             t2,
		T3:             t3,
		T4:             t3.Add(ms(rev)),
		SenderClock:    synced,
		ReflectorClock: synced,
		SenderTTL:      251,
		ReplyTTL:       252,
	}
}

func sendAll(t *testing.T, a *Accumulator, n int) {
	t.Helper()
	for i := range n {
		if !a.Sent(uint32(i), t0.Add(ms(float64(100*i)))) {
			t.Fatalf("Sent(%d) = false", i)
		}
	}
}

func wantDist(t *testing.T, name string, got, want Distribution) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
	}
}

// knownRun is five packets, 100 ms apart, of which seq 3 is lost on the
// forward path. The values are small enough to check by hand.
//
//	seq  fwd  refl  rev  rtt
//	0    10   1     10   20
//	1    15   1     12   27
//	2    12   2     12   24
//	3    lost
//	4    20   1     20   40
func knownRun(t *testing.T) *Accumulator {
	t.Helper()
	a := NewAccumulator(Config{})
	a.SetReflectorBase(0) // a new Full session
	sendAll(t, a, 5)
	for _, r := range []PacketRecord{
		reply(0, 0, 0, 10, 1, 10),
		reply(1, 1, 100, 15, 1, 12),
		reply(2, 2, 200, 12, 2, 12),
		reply(4, 3, 400, 20, 1, 20),
	} {
		if res := a.Add(r); res.Kind != KindAccepted || res.Reordered {
			t.Fatalf("Add(seq %d) = %+v", r.SenderSeq, res)
		}
	}
	return a
}

func TestKnownRun(t *testing.T) {
	a := knownRun(t)
	lost := a.Expire(t0.Add(ms(400)+DefaultTimeout), nil)
	if want := []Lost{{Seq: 3, T1: t0.Add(ms(300))}}; len(lost) != 1 || lost[0] != want[0] {
		t.Errorf("Expire lost %v, want %v", lost, want)
	}
	s := a.Summary()

	if s.Sent != 5 || s.Received != 4 || s.Lost != 1 || s.Pending != 0 || s.LossRatio != 0.2 {
		t.Errorf("counts = %+v", s)
	}
	if !s.LossSplitValid || s.LostForward != 1 || s.LostReverse != 0 {
		t.Errorf("loss split = %v %d %d", s.LossSplitValid, s.LostForward, s.LostReverse)
	}
	wantDist(t, "RTT", s.RTT, Distribution{
		Count: 4, Samples: 4,
		Min: ms(20), Max: ms(40), Mean: ms(27.75), StdDev: 8655441,
		Median: ms(25.5), P90: ms(36.1), P95: ms(38.05), P99: ms(39.61), P999: ms(39.961),
	})
	wantDist(t, "Forward", s.Forward, Distribution{
		Count: 4, Samples: 4,
		Min: ms(10), Max: ms(20), Mean: ms(14.25), StdDev: 4349329,
		Median: ms(13.5), P90: ms(18.5), P95: ms(19.25), P99: ms(19.85), P999: ms(19.985),
	})
	if s.Reverse.Mean != ms(13.5) || s.Reverse.Median != ms(12) {
		t.Errorf("Reverse = %+v", s.Reverse)
	}
	if s.ReflectorDelay.Min != ms(1) || s.ReflectorDelay.Max != ms(2) || s.ReflectorDelay.Mean != ms(1.25) {
		t.Errorf("ReflectorDelay = %+v", s.ReflectorDelay)
	}

	// IPDV pairs are (0,1) and (1,2); seq 3 is lost, so (2,3) and (3,4)
	// are undefined.
	j := s.Jitter
	if j.IPDVRTT.Count != 2 || j.IPDVRTT.Mean != ms(5) || j.IPDVRTT.Max != ms(7) || j.IPDVRTT.Min != ms(3) {
		t.Errorf("IPDVRTT = %+v", j.IPDVRTT)
	}
	if j.IPDVForward.Mean != ms(4) || j.IPDVForward.Max != ms(5) {
		t.Errorf("IPDVForward = %+v", j.IPDVForward)
	}
	if j.IPDVReverse.Mean != ms(1) || j.IPDVReverse.Max != ms(2) {
		t.Errorf("IPDVReverse = %+v", j.IPDVReverse)
	}
	wantDist(t, "PDV", j.PDV, Distribution{
		Count: 4, Samples: 4,
		Min: 0, Max: ms(20), Mean: ms(7.75), StdDev: 8655441,
		Median: ms(5.5), P90: ms(16.1), P95: ms(18.05), P99: ms(19.61), P999: ms(19.961),
	})
	// D = 7, 3, 16 ms in arrival order:
	// J = 7/16 = 0.4375; J += (3 − J)/16 → 0.59765625; J += (16 − J)/16 → 1.560302734375.
	if j.RFC3550 != 1560303 {
		t.Errorf("RFC3550 = %v", j.RFC3550)
	}
	if j.P95P50 != ms(12.55) {
		t.Errorf("P95P50 = %v", j.P95P50)
	}

	if !s.OWDValid || s.NegativeOWD != 0 || !s.SenderSynced || !s.ReflectorSynced {
		t.Errorf("clock = %+v", s)
	}
	if s.MaxClockError != 20*time.Microsecond {
		t.Errorf("MaxClockError = %v", s.MaxClockError)
	}
	if want := (Hops{Count: 4, Min: 4, Max: 4, Mode: 4}); s.ForwardHops != want {
		t.Errorf("ForwardHops = %+v", s.ForwardHops)
	}
	if want := (Hops{Count: 4, Min: 3, Max: 3, Mode: 3}); s.ReverseHops != want {
		t.Errorf("ReverseHops = %+v", s.ReverseHops)
	}
}

func TestSummaryIsSnapshot(t *testing.T) {
	a := knownRun(t)
	before := a.Summary()
	if again := a.Summary(); again != before {
		t.Fatalf("Summary changed the accumulator:\n%+v\n%+v", before, again)
	}
	// Nothing is retired yet, but IPDV already counts the received pairs.
	if before.Pending != 1 || before.Lost != 0 || before.LossRatio != 0 || before.LossSplitValid {
		t.Errorf("before expiry = %+v", before)
	}
	if before.Jitter.IPDVRTT.Count != 2 || before.Jitter.IPDVRTT.Mean != ms(5) {
		t.Errorf("IPDVRTT before expiry = %+v", before.Jitter.IPDVRTT)
	}

	// Retiring part of the queue must not change IPDV either.
	a.Expire(t0.Add(ms(150)+DefaultTimeout), nil)
	mid := a.Summary()
	if mid.Jitter != before.Jitter || mid.Pending != 1 {
		t.Errorf("after partial expiry = %+v", mid)
	}
	a.Expire(t0.Add(ms(400)+DefaultTimeout), nil)
	after := a.Summary()
	if after.Jitter != before.Jitter || after.Lost != 1 || after.Pending != 0 {
		t.Errorf("after expiry = %+v", after)
	}
}

func TestReorderRFC4737(t *testing.T) {
	// RFC 4737 §3.3: a packet is reordered when its sequence number is below
	// NextExp, the next expected number after the largest seen so far.
	tests := []struct {
		name    string
		arrival []uint32
		want    uint64
		flagged []uint32
	}{
		{"in order", []uint32{1, 2, 3, 4, 5}, 0, nil},
		{"one late packet", []uint32{1, 2, 3, 5, 6, 7, 8, 4, 9, 10}, 1, []uint32{4}},
		// One early packet makes every packet it overtook reordered.
		{"one early packet", []uint32{1, 2, 3, 4, 7, 5, 6, 8}, 2, []uint32{5, 6}},
		{"swap", []uint32{1, 3, 2, 4}, 1, []uint32{2}},
		{"reversed tail", []uint32{1, 4, 3, 2, 5}, 2, []uint32{3, 2}},
		{"gap is not reordering", []uint32{1, 2, 5, 6}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAccumulator(Config{StatelessReflector: true})
			for s := uint32(1); s <= 10; s++ {
				a.Sent(s, t0)
			}
			var flagged []uint32
			for i, s := range tt.arrival {
				r := reply(s, s, 0, 10, 0, float64(10+i))
				if res := a.Add(r); res.Reordered {
					flagged = append(flagged, s)
				}
			}
			if got := a.Summary().Reordered; got != tt.want {
				t.Errorf("Reordered = %d, want %d", got, tt.want)
			}
			if !equalSeqs(flagged, tt.flagged) {
				t.Errorf("flagged %v, want %v", flagged, tt.flagged)
			}
		})
	}
}

func equalSeqs(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReorderDirection(t *testing.T) {
	// Forward reordering: the reflector saw seq 1 first and numbered it 0.
	// The replies come back in reflection order.
	a := NewAccumulator(Config{})
	sendAll(t, a, 2)
	a.Add(reply(1, 0, 100, 5, 0, 5))
	a.Add(reply(0, 1, 0, 106, 0, 5))
	s := a.Summary()
	if s.Reordered != 1 || s.ReorderedReverse != 0 || !s.LossSplitValid {
		t.Errorf("forward: %+v", s)
	}

	// Reverse reordering: reflected in order, swapped on the way back.
	a = NewAccumulator(Config{})
	sendAll(t, a, 2)
	a.Add(reply(1, 1, 100, 5, 0, 5))
	a.Add(reply(0, 0, 0, 5, 0, 110))
	s = a.Summary()
	if s.Reordered != 1 || s.ReorderedReverse != 1 || !s.LossSplitValid {
		t.Errorf("reverse: %+v", s)
	}
}

func TestDuplicates(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 2)
	steps := []struct {
		r    PacketRecord
		want Kind
	}{
		{reply(0, 0, 0, 10, 1, 10), KindAccepted},
		{reply(0, 0, 0, 10, 1, 12), KindDupReverse},
		{reply(0, 1, 0, 11, 1, 10), KindDupForward},
		{reply(1, 2, 100, 10, 1, 10), KindAccepted},
		{reply(1, 2, 100, 10, 1, 11), KindDupReverse},
	}
	for i, st := range steps {
		if got := a.Add(st.r); got.Kind != st.want {
			t.Errorf("step %d: kind %v, want %v", i, got.Kind, st.want)
		}
	}
	s := a.Summary()
	if s.Received != 2 || s.DupForward != 1 || s.DupReverse != 2 {
		t.Errorf("summary = %+v", s)
	}
	// Only the first copy contributes delays (RFC 7679 §3.5).
	if s.RTT.Count != 2 || s.RTT.Max != ms(20) {
		t.Errorf("RTT = %+v", s.RTT)
	}
}

func TestStatelessReflector(t *testing.T) {
	a := NewAccumulator(Config{StatelessReflector: true})
	sendAll(t, a, 3)
	a.Add(reply(1, 1, 100, 10, 1, 10))
	a.Add(reply(0, 0, 0, 10, 1, 120))
	a.Add(reply(0, 0, 0, 10, 1, 121))
	a.Expire(t0.Add(ms(200)+DefaultTimeout), nil)
	s := a.Summary()
	if s.LossSplitValid || s.LostForward != 0 || s.LostReverse != 0 || s.ReorderedReverse != 0 {
		t.Errorf("split = %+v", s)
	}
	if s.Reordered != 1 || s.DupReverse != 1 || s.Lost != 1 {
		t.Errorf("counts = %+v", s)
	}
}

func TestReflectorRewind(t *testing.T) {
	tests := []struct {
		name string
		last PacketRecord
	}{
		// The counter restarted at 0 and the reply was reflected later.
		{"restart", reply(3, 0, 300, 10, 1, 10)},
		// Two packets can never share a number.
		{"repeat", reply(3, 2, 300, 10, 1, 10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAccumulator(Config{})
			sendAll(t, a, 4)
			for i := range uint32(3) {
				a.Add(reply(i, i, float64(100*i), 10, 1, 10))
			}
			a.Add(tt.last)
			a.Expire(t0.Add(ms(300)+DefaultTimeout), nil)
			s := a.Summary()
			if s.LossSplitValid || s.ReorderedReverse != 0 || s.Received != 4 || s.Lost != 0 {
				t.Errorf("summary = %+v", s)
			}
		})
	}
}

func TestLossSplitBaseline(t *testing.T) {
	// A Light reflector that kept its flow from an earlier run, or a later
	// window, numbers from above zero. With a loss the replies cannot tell
	// where it started.
	run := func(base *uint32) Summary {
		a := NewAccumulator(Config{})
		if base != nil {
			a.SetReflectorBase(*base)
		}
		sendAll(t, a, 3)
		a.Add(reply(0, 50, 0, 10, 1, 10))
		a.Add(reply(2, 51, 200, 10, 1, 10)) // seq 1 lost forward
		a.Expire(t0.Add(ms(200)+DefaultTimeout), nil)
		return a.Summary()
	}
	if s := run(nil); s.LossSplitValid {
		t.Errorf("unknown base: %+v", s)
	}
	fifty := uint32(50)
	if s := run(&fifty); !s.LossSplitValid || s.LostForward != 1 || s.LostReverse != 0 {
		t.Errorf("base 50: %+v", s)
	}

	// Without loss every reflection was seen: the split is trivially valid.
	a := NewAccumulator(Config{})
	sendAll(t, a, 2)
	a.Add(reply(0, 50, 0, 10, 1, 10))
	a.Add(reply(1, 51, 100, 10, 1, 10))
	a.Expire(t0.Add(ms(100)+DefaultTimeout), nil)
	if s := a.Summary(); !s.LossSplitValid || s.LostForward != 0 || s.LostReverse != 0 {
		t.Errorf("no loss: %+v", s)
	}

	// A later packet with a smaller number cannot be counted from a base.
	a = NewAccumulator(Config{})
	sendAll(t, a, 3)
	a.Add(reply(0, 9, 0, 10, 1, 10))
	a.Add(reply(2, 5, 200, 10, 1, 10))
	a.Expire(t0.Add(ms(200)+DefaultTimeout), nil)
	if s := a.Summary(); s.LossSplitValid {
		t.Errorf("summary = %+v", s)
	}
}

// TestHiddenTailReflection is the first case of the Codex re-review M5:
// S1 overtook S0 and took R0; S0 took R1 and its reply was lost. The reply
// to the last packet came, yet R1 is unseen, so the next window's base is
// unknown and its split is not claimed.
func TestHiddenTailReflection(t *testing.T) {
	prev := NewAccumulator(Config{})
	prev.SetReflectorBase(0)
	sendAll(t, prev, 2)
	prev.Add(reply(1, 0, 100, 10, 1, 10))
	prev.Expire(t0.Add(ms(100)+DefaultTimeout), nil)
	if base, ok := prev.NextReflectorBase(); ok {
		t.Fatalf("NextReflectorBase = %d, known", base)
	}
	// The next window: S2 lost forward, S3 took R2. Truth: forward 1.
	next := NewAccumulator(Config{})
	next.Sent(2, t0.Add(ms(200)))
	next.Sent(3, t0.Add(ms(300)))
	next.Add(reply(3, 2, 300, 10, 1, 10))
	next.Expire(t0.Add(ms(300)+DefaultTimeout), nil)
	if s := next.Summary(); s.LossSplitValid {
		t.Errorf("next window claims a split: %+v", s)
	}
}

// TestUnknownBaseReorderedFirst is the second case of the Codex re-review
// M5: in a window whose true base is R10, S11 overtook S10, took R10 and
// its reply was lost. The replies cannot rule that out, so with a loss and
// no given base the split is not claimed.
func TestUnknownBaseReorderedFirst(t *testing.T) {
	a := NewAccumulator(Config{})
	for i := uint32(10); i < 13; i++ {
		a.Sent(i, t0.Add(ms(float64(100*i))))
	}
	a.Add(reply(10, 11, 1000, 10, 1, 10))
	a.Add(reply(12, 12, 1200, 10, 1, 10))
	a.Expire(t0.Add(ms(1200)+DefaultTimeout), nil)
	if s := a.Summary(); s.LossSplitValid {
		t.Errorf("split claimed from an inferred base: %+v", s)
	}
	// Told the base, the gap at R10 is a reverse loss.
	b := NewAccumulator(Config{})
	b.SetReflectorBase(10)
	for i := uint32(10); i < 13; i++ {
		b.Sent(i, t0.Add(ms(float64(100*i))))
	}
	b.Add(reply(10, 11, 1000, 10, 1, 10))
	b.Add(reply(12, 12, 1200, 10, 1, 10))
	b.Expire(t0.Add(ms(1200)+DefaultTimeout), nil)
	if s := b.Summary(); !s.LossSplitValid || s.LostForward != 0 || s.LostReverse != 1 {
		t.Errorf("known base: %+v", s)
	}
}

func TestLateAndStray(t *testing.T) {
	a := NewAccumulator(Config{Timeout: time.Second})
	sendAll(t, a, 3)

	if got := a.Add(reply(7, 0, 0, 10, 1, 10)); got.Kind != KindStray {
		t.Errorf("unsent seq: %v", got.Kind)
	}
	// Seq 0 replies after its deadline: lost, even before Expire runs.
	if got := a.Add(reply(0, 0, 0, 600, 1, 500)); got.Kind != KindLate {
		t.Errorf("late reply: %v", got.Kind)
	}
	if got := a.Add(reply(0, 0, 0, 10, 1, 10)); got.Kind != KindLate {
		t.Errorf("reply after a late one: %v", got.Kind)
	}
	// Seq 1 is received in time; a copy after its deadline is late, not a
	// duplicate.
	a.Add(reply(1, 1, 100, 10, 1, 10))
	if got := a.Add(reply(1, 1, 100, 10, 1, 1000)); got.Kind != KindLate {
		t.Errorf("late copy: %v", got.Kind)
	}
	a.Expire(t0.Add(ms(200)+time.Second), nil)
	// After retirement the packet is unknown.
	if got := a.Add(reply(2, 2, 200, 10, 1, 10)); got.Kind != KindStray {
		t.Errorf("retired seq: %v", got.Kind)
	}
	s := a.Summary()
	if s.Sent != 3 || s.Received != 1 || s.Lost != 2 || s.Late != 3 || s.Stray != 2 || s.DupReverse != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestBoundaries(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		s := NewAccumulator(Config{}).Summary()
		if s != (Summary{}) {
			t.Errorf("empty summary = %+v", s)
		}
	})
	t.Run("one sample", func(t *testing.T) {
		a := NewAccumulator(Config{})
		sendAll(t, a, 1)
		a.Add(reply(0, 0, 0, 10, 1, 10))
		a.Expire(t0.Add(DefaultTimeout), nil)
		s := a.Summary()
		v := ms(20)
		wantDist(t, "RTT", s.RTT, Distribution{
			Count: 1, Samples: 1, Min: v, Max: v, Mean: v,
			Median: v, P90: v, P95: v, P99: v, P999: v,
		})
		if s.Jitter.IPDVRTT.Count != 0 || s.Jitter.RFC3550 != 0 || s.Jitter.P95P50 != 0 {
			t.Errorf("jitter = %+v", s.Jitter)
		}
		if s.Jitter.PDV.Max != 0 || s.Jitter.PDV.Count != 1 {
			t.Errorf("PDV = %+v", s.Jitter.PDV)
		}
	})
	t.Run("all lost", func(t *testing.T) {
		a := NewAccumulator(Config{})
		sendAll(t, a, 4)
		if !a.Unsent(3) {
			t.Fatal("Unsent(3) = false")
		}
		a.Expire(t0.Add(ms(200)+DefaultTimeout), nil)
		s := a.Summary()
		if s.Sent != 3 || s.SendErrors != 1 || s.Lost != 3 || s.LossRatio != 1 || s.Received != 0 {
			t.Errorf("counts = %+v", s)
		}
		if s.RTT.Count != 0 || s.OWDValid || s.SenderSynced || s.LossSplitValid || s.ForwardHops.Count != 0 {
			t.Errorf("summary = %+v", s)
		}
	})
	t.Run("aborted", func(t *testing.T) {
		a := NewAccumulator(Config{})
		sendAll(t, a, 4)
		a.Add(reply(0, 0, 0, 10, 1, 10))
		a.Expire(t0.Add(ms(100)+DefaultTimeout), nil)
		s := a.Summary()
		// Seq 1 timed out; seq 2 and 3 were cut short and are neither.
		if s.Lost != 1 || s.Pending != 2 || s.LossRatio != 0.5 {
			t.Errorf("summary = %+v", s)
		}
	})
	t.Run("duplicate send", func(t *testing.T) {
		a := NewAccumulator(Config{})
		sendAll(t, a, 1)
		if a.Sent(0, t0) {
			t.Error("second Sent(0) = true")
		}
		if s := a.Summary(); s.Sent != 1 {
			t.Errorf("Sent = %d", s.Sent)
		}
	})
}

func TestClockValidity(t *testing.T) {
	unsynced := func(r PacketRecord) PacketRecord { r.ReflectorClock.Synced = false; return r }
	unknown := func(r PacketRecord) PacketRecord { r.SenderClock.ErrorUnknown = true; return r }
	ptp := func(r PacketRecord) PacketRecord { r.ReflectorClock.UnsupportedFormat = true; return r }

	tests := []struct {
		name       string
		r          PacketRecord
		owdValid   bool
		negative   uint64
		rttCount   uint64
		reflSynced bool
	}{
		{"synced", reply(0, 0, 0, 10, 1, 10), true, 0, 1, true},
		{"reflector unsynced", unsynced(reply(0, 0, 0, 10, 1, 10)), false, 0, 1, false},
		{"zero multiplier", unknown(reply(0, 0, 0, 10, 1, 10)), false, 0, 1, true},
		// The reflector's clock is 15 ms behind: forward delay −5 ms.
		{"negative forward", reply(0, 0, 0, -5, 1, 25), false, 1, 1, true},
		{"negative reverse", reply(0, 0, 0, 25, 1, -5), false, 1, 1, true},
		{"unsupported format", ptp(reply(0, 0, 0, 10, 1, 10)), false, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAccumulator(Config{})
			sendAll(t, a, 1)
			a.Add(tt.r)
			s := a.Summary()
			if s.OWDValid != tt.owdValid || s.NegativeOWD != tt.negative || s.RTT.Count != tt.rttCount ||
				(s.Unsupported == 1) != (tt.rttCount == 0) {
				t.Errorf("summary = %+v", s)
			}
			if s.Received != 1 || s.ReflectorSynced != tt.reflSynced || !s.SenderSynced {
				t.Errorf("counts = %+v", s)
			}
		})
	}
}

func TestDecimatedRun(t *testing.T) {
	a := NewAccumulator(Config{MaxSamples: 8, Timeout: time.Second})
	const n = 100
	for i := range n {
		a.Sent(uint32(i), t0.Add(ms(float64(i))))
		a.Add(reply(uint32(i), uint32(i), float64(i), float64(i), 0, 0))
		a.Expire(t0.Add(ms(float64(i))), nil)
	}
	a.Expire(t0.Add(ms(n)+time.Second), nil)
	s := a.Summary()
	if s.RTT.Count != n || s.RTT.Samples > 8 || s.RTT.Samples < 4 {
		t.Errorf("RTT = %+v", s.RTT)
	}
	// Moments and extremes stay exact.
	if s.RTT.Min != 0 || s.RTT.Max != ms(n-1) || s.RTT.Mean != ms(49.5) {
		t.Errorf("RTT = %+v", s.RTT)
	}
	if s.Jitter.IPDVRTT.Count != n-1 || s.Jitter.IPDVRTT.Mean != ms(1) {
		t.Errorf("IPDVRTT = %+v", s.Jitter.IPDVRTT)
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		KindAccepted:   "accepted",
		KindDupForward: "duplicate_forward",
		KindDupReverse: "duplicate_reverse",
		KindLate:       "late",
		KindStray:      "stray",
		Kind(99):       "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}

// FuzzAccumulator checks that every sent packet ends up in exactly one of
// received, lost and pending, whatever the order of events.
func FuzzAccumulator(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{0x10, 0x11, 0x81, 0x80, 0xc0, 0x12, 0x92, 0xff})
	f.Fuzz(func(t *testing.T, ops []byte) {
		a := NewAccumulator(Config{Timeout: 50 * time.Millisecond, MaxSamples: 4})
		now := t0
		var seq uint32
		for _, op := range ops {
			now = now.Add(time.Duration(op&0x0f) * time.Millisecond)
			switch op >> 6 {
			case 0:
				a.Sent(seq, now)
				seq++
			case 1, 2:
				s := uint32(op>>4) & 3
				if seq > 0 {
					s = seq - 1 - s%seq
				}
				r := reply(s, uint32(op)&7, 0, 0, 0, 0)
				r.T1, r.T2, r.T3, r.T4 = now.Add(-ms(40)), now.Add(-ms(20)), now.Add(-ms(19)), now
				a.Add(r)
			case 3:
				if op&0x30 == 0x30 && seq > 0 {
					a.Unsent(seq - 1)
				} else {
					a.Expire(now, nil)
				}
			}
			s := a.Summary()
			if s.Pending != a.Pending() {
				t.Fatalf("Pending() = %d, summary %d", a.Pending(), s.Pending)
			}
			if s.Received+s.Lost+s.Pending != s.Sent {
				t.Fatalf("received %d + lost %d + pending %d != sent %d", s.Received, s.Lost, s.Pending, s.Sent)
			}
			if s.LossSplitValid && s.LostForward+s.LostReverse != s.Lost {
				t.Fatalf("split %d + %d != lost %d", s.LostForward, s.LostReverse, s.Lost)
			}
		}
	})
}

func TestUnsent(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 4)
	a.Add(reply(0, 0, 0, 10, 1, 10))
	if a.Unsent(0) {
		t.Error("Unsent of a received packet = true")
	}
	if !a.Unsent(1) || a.Unsent(1) {
		t.Error("Unsent(1) twice: want true then false")
	}
	if a.Unsent(9) {
		t.Error("Unsent of an unknown packet = true")
	}
	if a.Pending() != 2 {
		t.Errorf("Pending() = %d", a.Pending())
	}
	if got := a.Add(reply(1, 1, 100, 10, 1, 10)); got.Kind != KindStray {
		t.Errorf("reply to an unsent packet: %v", got.Kind)
	}
	a.Add(reply(2, 1, 200, 10, 1, 10))
	a.Add(reply(3, 2, 300, 10, 1, 10))
	lost := a.Expire(t0.Add(ms(300)+DefaultTimeout), nil)
	s := a.Summary()
	if len(lost) != 0 || s.Sent != 3 || s.SendErrors != 1 || s.Received != 3 || s.Lost != 0 || s.Pending != 0 {
		t.Errorf("lost %v, summary %+v", lost, s)
	}
	// (0,1) and (1,2) are broken by the unsent packet; (2,3) remains.
	if s.Jitter.IPDVRTT.Count != 1 {
		t.Errorf("IPDVRTT = %+v", s.Jitter.IPDVRTT)
	}
}

func TestNegativeRTT(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 2)
	// The sender's clock stepped back 50 ms between T1 and T4.
	if res := a.Add(reply(0, 0, 0, 10, 1, -40)); res.Kind != KindAccepted || res.Metrics.RTT >= 0 {
		t.Fatalf("Add = %+v", res)
	}
	a.Add(reply(1, 1, 100, 10, 1, 10))
	s := a.Summary()
	if s.InvalidRTT != 1 || s.Received != 2 || s.RTT.Count != 1 || s.Jitter.IPDVRTT.Count != 0 {
		t.Errorf("summary = %+v", s)
	}
}

// BenchmarkPacket is the per-packet work of a run: send, reply, retire.
func BenchmarkPacket(b *testing.B) {
	a := NewAccumulator(Config{MaxSamples: 1024})
	var seq uint32
	b.ReportAllocs()
	for b.Loop() {
		send := float64(seq) * 10
		a.Sent(seq, t0.Add(ms(send)))
		a.Add(reply(seq, seq, send, 5, 0.1, 5))
		a.Expire(t0.Add(ms(send)+DefaultTimeout), nil)
		seq++
	}
}

// BenchmarkSummary summarizes a run of 1000 packets.
func BenchmarkSummary(b *testing.B) {
	a := NewAccumulator(Config{})
	for i := range uint32(1000) {
		a.Sent(i, t0.Add(ms(float64(i))))
		a.Add(reply(i, i, float64(i), 5+float64(i%7), 0.1, 5))
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = a.Summary()
	}
}

// TestCopySenderLossIsAmbiguous: a reflector echoing R = S with loss may be
// stateless, so the direction of the loss is not estimated.
func TestCopySenderLossIsAmbiguous(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 4)
	for _, s := range []uint32{0, 1, 3} {
		a.Add(reply(s, s, float64(100*s), 10, 1, 10))
	}
	a.Expire(t0.Add(ms(300)+DefaultTimeout), nil)
	if s := a.Summary(); s.LossSplitValid || s.Lost != 1 {
		t.Errorf("summary = %+v", s)
	}

	// Without loss R = S says nothing either way, and the split is valid.
	a = NewAccumulator(Config{})
	sendAll(t, a, 2)
	a.Add(reply(0, 0, 0, 10, 1, 10))
	a.Add(reply(1, 1, 100, 10, 1, 10))
	a.Expire(t0.Add(ms(100)+DefaultTimeout), nil)
	if s := a.Summary(); !s.LossSplitValid {
		t.Errorf("summary = %+v", s)
	}
}

func TestResent(t *testing.T) {
	a := NewAccumulator(Config{Timeout: time.Second})
	sendAll(t, a, 2)
	if !a.Resent(1, t0.Add(ms(150))) {
		t.Fatal("Resent(1) = false")
	}
	a.Add(reply(0, 0, 0, 10, 1, 10))
	if a.Resent(0, t0) || a.Resent(9, t0) {
		t.Error("Resent of a received or unknown packet = true")
	}
	// The deadline moved with T1: 1.15 s, not 1.1 s.
	lost := a.Expire(t0.Add(ms(1120)), nil)
	if len(lost) != 0 {
		t.Errorf("lost before the moved deadline: %v", lost)
	}
	lost = a.Expire(t0.Add(ms(1150)), nil)
	if len(lost) != 1 || !lost[0].T1.Equal(t0.Add(ms(150))) {
		t.Errorf("lost = %v", lost)
	}
}

// TestLateIsReportedLost: a packet lost through a late reply is reported by
// Expire like any other (review m1).
func TestLateIsReportedLost(t *testing.T) {
	a := NewAccumulator(Config{Timeout: time.Second})
	sendAll(t, a, 2)
	if got := a.Add(reply(0, 0, 0, 1000, 1, 500)); got.Kind != KindLate {
		t.Fatalf("Add = %v", got.Kind)
	}
	lost := a.Expire(t0.Add(ms(100)+time.Second), nil)
	if len(lost) != 2 || lost[0].Seq != 0 || lost[1].Seq != 1 {
		t.Errorf("lost = %v", lost)
	}
	if s := a.Summary(); s.Lost != 2 || s.Pending != 0 {
		t.Errorf("summary = %+v", s)
	}
}

// TestHeadForwardLoss: when the first packet is lost forward, nothing in
// the replies tells where the reflector started, so the direction of loss
// is only given when the session tells the base (Codex review M5).
func TestHeadForwardLoss(t *testing.T) {
	run := func(base *uint32) Summary {
		a := NewAccumulator(Config{})
		if base != nil {
			a.SetReflectorBase(*base)
		}
		sendAll(t, a, 3)
		a.Add(reply(1, 0, 100, 10, 1, 10))
		a.Add(reply(2, 1, 200, 10, 1, 10))
		a.Expire(t0.Add(ms(200)+DefaultTimeout), nil)
		return a.Summary()
	}
	if s := run(nil); s.LossSplitValid {
		t.Errorf("unknown base: %+v", s)
	}
	zero := uint32(0)
	if s := run(&zero); !s.LossSplitValid || s.LostForward != 1 || s.LostReverse != 0 {
		t.Errorf("new session: %+v", s)
	}
}

// TestDuplicateOfDuplicate: a reverse-path copy of a forward-path duplicate
// is a reverse duplicate, not another forward one.
func TestDuplicateOfDuplicate(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 1)
	kinds := []Kind{}
	for _, r := range []uint32{0, 1, 1, 2, 0, 3, 4, 5, 5} {
		kinds = append(kinds, a.Add(reply(0, r, 0, 10, 1, 10)).Kind)
	}
	want := []Kind{
		KindAccepted, KindDupForward, KindDupReverse, KindDupForward, KindDupReverse,
		KindDupForward, KindDupForward, KindDupForward, KindDupReverse, // a fourth copy too
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds %v, want %v", kinds, want)
	}
	if s := a.Summary(); s.DupForward != 5 || s.DupReverse != 3 {
		t.Errorf("summary = %+v", s)
	}
}

// TestKnownReflectorBase: a later window that loses its first packet
// forward is told the base by the previous window.
func TestKnownReflectorBase(t *testing.T) {
	prev := NewAccumulator(Config{})
	sendAll(t, prev, 2)
	prev.Add(reply(0, 0, 0, 10, 1, 10))
	prev.Add(reply(1, 1, 100, 10, 1, 10))
	base, ok := prev.NextReflectorBase()
	if !ok || base != 2 {
		t.Fatalf("NextReflectorBase = %d, %v", base, ok)
	}

	a := NewAccumulator(Config{})
	for i := uint32(2); i < 5; i++ {
		a.Sent(i, t0.Add(ms(float64(100*i))))
	}
	// Seq 2 is lost forward; seq 3 and 4 become R 2 and 3.
	a.Add(reply(3, 2, 300, 10, 1, 10))
	a.Add(reply(4, 3, 400, 10, 1, 10))
	a.Expire(t0.Add(ms(400)+DefaultTimeout), nil)
	if s := a.Summary(); s.LossSplitValid {
		t.Fatalf("split without a known base: %+v", s)
	}
	a.SetReflectorBase(base)
	if s := a.Summary(); !s.LossSplitValid || s.LostForward != 1 || s.LostReverse != 0 {
		t.Errorf("summary = %+v", s)
	}

	for _, c := range []*Accumulator{NewAccumulator(Config{}), NewAccumulator(Config{StatelessReflector: true})} {
		if _, ok := c.NextReflectorBase(); ok {
			t.Error("NextReflectorBase known without replies or with a stateless reflector")
		}
	}
}

// TestWindowBoundary covers the boundary between two windows of a session
// (Codex review M5): a known base, the last reply of the previous window
// missing, and a packet overtaken across the boundary.
func TestWindowBoundary(t *testing.T) {
	window := func(first uint32, replies [][2]uint32) *Accumulator {
		a := NewAccumulator(Config{})
		for i := range uint32(3) {
			a.Sent(first+i, t0.Add(ms(float64(100*(first+i)))))
		}
		for _, r := range replies {
			a.Add(reply(r[0], r[1], float64(100*r[0]), 10, 1, 10))
		}
		a.Expire(t0.Add(ms(float64(100*(first+2)))+DefaultTimeout), nil)
		return a
	}

	// The last reply of the previous window is missing: S2 may have been
	// reflected as R2 and lost back, or lost forward. The next base is
	// unknown, and the next window cannot be split.
	prev := window(0, [][2]uint32{{0, 0}, {1, 1}})
	if _, ok := prev.NextReflectorBase(); ok {
		t.Error("next base known without the last reply")
	}
	next := window(3, [][2]uint32{{4, 3}, {5, 4}})
	if s := next.Summary(); s.LossSplitValid {
		t.Errorf("split without a base: %+v", s)
	}

	// With the last reply, the next window starts at R 13: S3 lost forward.
	// (The reflector numbers from 10 here, so that R = S cannot be a
	// stateless reflector's echo.)
	prev = window(0, [][2]uint32{{0, 10}, {1, 11}, {2, 12}})
	base, ok := prev.NextReflectorBase()
	if !ok || base != 13 {
		t.Fatalf("NextReflectorBase = %d, %v", base, ok)
	}
	next = window(3, [][2]uint32{{4, 13}, {5, 14}})
	next.SetReflectorBase(base)
	if s := next.Summary(); !s.LossSplitValid || s.LostForward != 1 || s.LostReverse != 0 {
		t.Errorf("head loss: %+v", s)
	}

	// A gap in R inside the window is reverse loss.
	next = window(3, [][2]uint32{{3, 13}, {5, 15}})
	next.SetReflectorBase(base)
	if s := next.Summary(); !s.LossSplitValid || s.LostForward != 0 || s.LostReverse != 1 {
		t.Errorf("reverse loss: %+v", s)
	}

	// S3 overtook S2 of the previous window on the way out and took R 12:
	// the windows are mixed up, so neither direction is claimed.
	next = window(3, [][2]uint32{{3, 12}, {4, 14}, {5, 15}})
	next.SetReflectorBase(base)
	if s := next.Summary(); s.LossSplitValid {
		t.Errorf("crossed boundary: %+v", s)
	}
}

// TestDSCPReflected: the DSCP and ECN the reflector saw (RFC 7750).
func TestDSCPReflected(t *testing.T) {
	a := NewAccumulator(Config{SendDSCP: 46})
	sendAll(t, a, 5)
	octets := []uint8{46 << 2, 46<<2 | 3, 0, 46<<2 | 1}
	for i, o := range octets {
		r := reply(uint32(i), uint32(i), float64(100*i), 10, 1, 10)
		r.DSCPECN, r.HasDSCPECN = o, true
		a.Add(r)
	}
	a.Add(reply(4, 4, 400, 10, 1, 10)) // a reflector without RFC 7750
	want := DSCPReflected{Count: 4, Mode: 46, Remarked: 1, CE: 1}
	if got := a.Summary().DSCPReflected; got != want {
		t.Errorf("DSCPReflected = %+v, want %+v", got, want)
	}
	if got := NewAccumulator(Config{}).Summary().DSCPReflected; got != (DSCPReflected{}) {
		t.Errorf("empty = %+v", got)
	}
}

// TestClockErrorUnknown: an end with Multiplier 0 makes the error bound
// unknown (audit U7).
func TestClockErrorUnknown(t *testing.T) {
	a := NewAccumulator(Config{})
	sendAll(t, a, 2)
	a.Add(reply(0, 0, 0, 10, 1, 10))
	if s := a.Summary(); s.MaxClockErrorUnknown {
		t.Error("unknown with known estimates")
	}
	r := reply(1, 1, 100, 10, 1, 10)
	r.ReflectorClock.ErrorUnknown = true
	if m := a.Add(r).Metrics; !m.ClockErrorUnknown || m.MaxClockError != 10*time.Microsecond {
		t.Errorf("metrics = %+v", m)
	}
	if s := a.Summary(); !s.MaxClockErrorUnknown || s.MaxClockError != 20*time.Microsecond {
		t.Errorf("summary = %+v", s)
	}
}

// TestReorderAcrossWrap: sequence numbers that wrap past 2^32 within a run
// or window are in order, in both directions; a packet that comes late
// across the wrap is still reordered (maintenance review A).
func TestReorderAcrossWrap(t *testing.T) {
	seqs := []uint32{math.MaxUint32 - 1, math.MaxUint32, 0, 1}
	a := NewAccumulator(Config{})
	for _, s := range seqs {
		a.Sent(s, t0)
	}
	for i, s := range seqs {
		if res := a.Add(reply(s, s, 0, 10, 0, float64(10+i))); res.Reordered {
			t.Errorf("seq %d flagged reordered", s)
		}
	}
	if s := a.Summary(); s.Reordered != 0 || s.ReorderedReverse != 0 {
		t.Errorf("in-order wrap: reordered %d, reverse %d", s.Reordered, s.ReorderedReverse)
	}

	b := NewAccumulator(Config{StatelessReflector: true})
	for _, s := range seqs {
		b.Sent(s, t0)
	}
	var flagged []uint32
	for i, s := range []uint32{math.MaxUint32 - 1, 0, math.MaxUint32, 1} {
		if res := b.Add(reply(s, s, 0, 10, 0, float64(10+i))); res.Reordered {
			flagged = append(flagged, s)
		}
	}
	if !equalSeqs(flagged, []uint32{math.MaxUint32}) {
		t.Errorf("flagged %v, want [%d]", flagged, uint32(math.MaxUint32))
	}
}
