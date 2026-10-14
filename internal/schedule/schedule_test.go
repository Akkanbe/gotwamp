package schedule

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// fakeRunner records the runs and carries them out with do.
type fakeRunner struct {
	do func(ctx context.Context, spec *SessionSpec, hooks sender.Hooks, n int) (sender.Summary, error)

	mu     sync.Mutex
	starts map[string][]time.Time
}

func newFakeRunner(do func(ctx context.Context, spec *SessionSpec, hooks sender.Hooks, n int) (sender.Summary, error)) *fakeRunner {
	return &fakeRunner{do: do, starts: map[string][]time.Time{}}
}

func (f *fakeRunner) Run(ctx context.Context, spec *SessionSpec, hooks sender.Hooks) (sender.Summary, error) {
	f.mu.Lock()
	n := len(f.starts[spec.Name])
	f.starts[spec.Name] = append(f.starts[spec.Name], time.Now())
	f.mu.Unlock()
	return f.do(ctx, spec, hooks, n)
}

// offsets returns the run starts of session relative to t0.
func (f *fakeRunner) offsets(session string, t0 time.Time) []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []time.Duration
	for _, s := range f.starts[session] {
		out = append(out, s.Sub(t0))
	}
	return out
}

// lasting runs for d, then reports a summary.
func lasting(d time.Duration) func(context.Context, *SessionSpec, sender.Hooks, int) (sender.Summary, error) {
	return func(ctx context.Context, _ *SessionSpec, hooks sender.Hooks, _ int) (sender.Summary, error) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			s := sender.Summary{EndedAt: time.Now(), Aborted: true}
			hooks.Summary(s)
			return s, ctx.Err()
		case <-t.C:
		}
		hooks.Packet(sender.PacketResult{})
		hooks.Lost(sender.LostPacket{Seq: 1})
		s := sender.Summary{EndedAt: time.Now()}
		hooks.Summary(s)
		return s, nil
	}
}

type recordSink struct {
	mu     sync.Mutex
	events []store.Event
}

func (r *recordSink) Publish(e store.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordSink) count(k store.EventKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Kind == k {
			n++
		}
	}
	return n
}

func (r *recordSink) of(k store.EventKind) []store.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.Event
	for _, e := range r.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func periodic(name string, freq time.Duration) SessionSpec {
	return SessionSpec{
		Name:     name,
		Schedule: Schedule{Type: Periodic, Frequency: freq},
		Info:     store.SessionInfo{Target: "192.0.2.10", Mode: "light"},
		Test:     sender.Config{Count: 10},
	}
}

func secs(v ...float64) []time.Duration {
	out := make([]time.Duration, len(v))
	for i, s := range v {
		out[i] = time.Duration(s * float64(time.Second))
	}
	return out
}

func TestPeriodic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(time.Second))
		sink := &recordSink{}
		s := New(Config{Runner: r, Sink: sink})
		t0 := time.Now()
		if _, err := s.Update([]SessionSpec{periodic("a", 10*time.Second)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if got := r.offsets("a", t0); !slices.Equal(got, secs(0, 10, 20, 30)) {
			t.Errorf("runs at %v", got)
		}
		st, err := s.Get("a")
		if err != nil || st.RunsOK != 4 || st.Running || st.State != StateRunning || !st.LastRun.Equal(t0.Add(31*time.Second)) {
			t.Errorf("status %+v, %v", st, err)
		}
		if sink.count(store.EventRunSummary) != 4 || sink.count(store.EventPacket) != 4 || sink.count(store.EventLost) != 4 {
			t.Errorf("events: %d summaries", sink.count(store.EventRunSummary))
		}
		ev := sink.of(store.EventRunSummary)
		if ev[0].Session.Name != "a" || ev[0].Session.Target != "192.0.2.10" || len(ev[0].RunID) != 26 || ev[0].RunID == ev[1].RunID {
			t.Errorf("event %+v", ev[0])
		}
		s.Close()
	})
}

func TestPeriodicSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(25 * time.Second))
		sink := &recordSink{}
		s := New(Config{Runner: r, Sink: sink})
		t0 := time.Now()
		if _, err := s.Update([]SessionSpec{periodic("a", 10*time.Second)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if got := r.offsets("a", t0); !slices.Equal(got, secs(0, 30)) {
			t.Errorf("runs at %v", got)
		}
		st, _ := s.Get("a")
		if st.Skipped != 2 || sink.count(store.EventRunSkipped) != 2 || !st.Running {
			t.Errorf("status %+v", st)
		}
		// Close aborts the run in progress and waits for it.
		s.Close()
		if got := sink.of(store.EventRunSummary); len(got) != 2 || !got[1].Summary.Aborted {
			t.Errorf("summaries %+v", got)
		}
		if st.RunsOK != 1 {
			t.Errorf("RunsOK = %d", st.RunsOK)
		}
	})
}

func TestJitter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(time.Second))
		// 3e9 ns in the first 8 octets: the first run waits 3 s of 10.
		seed := []byte{0, 0, 0, 0, 0xb2, 0xd0, 0x5e, 0x00}
		s := New(Config{Runner: r, rand: bytes.NewReader(seed)})
		spec := periodic("a", 60*time.Second)
		spec.Schedule.Jitter = 10 * time.Second
		t0 := time.Now()
		if err := s.Add(spec); err != nil {
			t.Fatal(err)
		}
		time.Sleep(70 * time.Second)
		synctest.Wait()
		if got := r.offsets("a", t0); !slices.Equal(got, secs(3, 63)) {
			t.Errorf("runs at %v", got)
		}
		s.Close()
	})
}

func TestRunErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(func(_ context.Context, _ *SessionSpec, _ sender.Hooks, n int) (sender.Summary, error) {
			switch n {
			case 0:
				return sender.Summary{}, &StageError{Stage: "request", Accept: 5, Err: errors.New("temporary resource limitation")}
			case 1:
				return sender.Summary{}, errors.New("socket gone")
			default:
				panic("boom")
			}
		})
		sink := &recordSink{}
		s := New(Config{Runner: r, Sink: sink})
		if err := s.Add(periodic("a", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Second)
		synctest.Wait()
		ev := sink.of(store.EventRunError)
		if len(ev) != 3 {
			t.Fatalf("%d errors", len(ev))
		}
		want := []store.RunError{
			{Stage: "request", Accept: 5, Message: "request: temporary resource limitation"},
			{Stage: "test", Message: "socket gone"},
			{Stage: "internal", Message: "internal: panic: boom"},
		}
		for i, e := range ev {
			if *e.Error != want[i] {
				t.Errorf("error %d = %+v", i, *e.Error)
			}
		}
		st, _ := s.Get("a")
		if st.RunsFailed != 3 || st.LastError != "internal: panic: boom" {
			t.Errorf("status %+v", st)
		}
		s.Close()
	})
}

func TestContinuous(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(func(ctx context.Context, spec *SessionSpec, hooks sender.Hooks, n int) (sender.Summary, error) {
			if spec.Test.Count != 0 || spec.Test.Window != time.Second {
				t.Errorf("test config %+v", spec.Test)
			}
			if n < 2 {
				return sender.Summary{}, errors.New("control connection refused")
			}
			for w := 0; ; w++ {
				select {
				case <-ctx.Done():
					s := sender.Summary{Window: w, EndedAt: time.Now(), Aborted: true}
					hooks.Summary(s)
					return s, ctx.Err()
				case <-time.After(time.Second):
					hooks.Summary(sender.Summary{Window: w, EndedAt: time.Now()})
				}
			}
		})
		sink := &recordSink{}
		s := New(Config{Runner: r, Sink: sink, retryDelay: 2 * time.Second})
		spec := SessionSpec{Name: "c", Schedule: Schedule{Type: Continuous, Window: time.Second}, Test: sender.Config{Count: 100}}
		t0 := time.Now()
		if _, err := s.Update([]SessionSpec{spec}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(7500 * time.Millisecond)
		synctest.Wait()
		// Two failures, retried 2 s apart; the third run lasts.
		if got := r.offsets("c", t0); !slices.Equal(got, secs(0, 2, 4)) {
			t.Errorf("runs at %v", got)
		}
		if n := sink.count(store.EventWindowSummary); n != 3 {
			t.Errorf("%d window summaries", n)
		}
		if err := s.Stop("c"); err != nil {
			t.Fatal(err)
		}
		ws := sink.of(store.EventWindowSummary)
		if len(ws) != 4 || !ws[3].Summary.Aborted || ws[3].Summary.Window != 3 {
			t.Errorf("windows %+v", ws)
		}
		st, _ := s.Get("c")
		if st.State != StateStopped || st.RunsOK != 3 || st.RunsFailed != 2 || st.LastError != "" {
			t.Errorf("status %+v", st)
		}
		s.Close()
	})
}

func TestUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(time.Second))
		s := New(Config{Runner: r})
		t0 := time.Now()
		ch, err := s.Update([]SessionSpec{periodic("a", 10*time.Second), periodic("b", 10*time.Second)})
		if err != nil || !slices.Equal(ch.Added, []string{"a", "b"}) {
			t.Fatalf("first update: %+v, %v", ch, err)
		}
		synctest.Wait()
		if err := s.Stop("b"); err != nil {
			t.Fatal(err)
		}
		if err := s.Add(periodic("api", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)

		changed := periodic("b", 20*time.Second)
		ch, err = s.Update([]SessionSpec{periodic("a", 10*time.Second), changed, periodic("c", 10*time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		want := Changes{Added: []string{"c"}, Changed: []string{"b"}, Unchanged: []string{"a"}}
		if !slices.Equal(ch.Added, want.Added) || !slices.Equal(ch.Changed, want.Changed) ||
			!slices.Equal(ch.Unchanged, want.Unchanged) || len(ch.Removed) != 0 {
			t.Errorf("changes %+v", ch)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		// a kept its schedule; b was recreated stopped; c started at 5 s.
		if got := r.offsets("a", t0); !slices.Equal(got, secs(0, 10)) {
			t.Errorf("a runs at %v", got)
		}
		if got := r.offsets("b", t0); !slices.Equal(got, secs(0)) {
			t.Errorf("b runs at %v", got)
		}
		if got := r.offsets("c", t0); !slices.Equal(got, secs(5, 15)) {
			t.Errorf("c runs at %v", got)
		}
		if st, _ := s.Get("b"); st.State != StateStopped || st.Spec.Schedule.Frequency != 20*time.Second {
			t.Errorf("b = %+v", st)
		}

		ch, err = s.Update([]SessionSpec{periodic("c", 10*time.Second)})
		if err != nil || !slices.Equal(ch.Removed, []string{"a", "b"}) {
			t.Errorf("removal: %+v, %v", ch, err)
		}
		var names []string
		for _, st := range s.List() {
			names = append(names, st.Spec.Name)
		}
		if !slices.Equal(names, []string{"api", "c"}) {
			t.Errorf("sessions %v", names)
		}
		if st, _ := s.Get("api"); st.Spec.Source != SourceAPI {
			t.Errorf("api source %v", st.Spec.Source)
		}

		bad := [][]SessionSpec{
			{periodic("api", time.Second)},
			{periodic("x", time.Second), periodic("x", time.Second)},
			{periodic("", time.Second)},
			{periodic("x", 0)},
			{{Name: "x", Schedule: Schedule{Type: Continuous}}},
			{{Name: "x"}},
			{{Name: "x", Schedule: Schedule{Type: Periodic, Frequency: time.Second, Jitter: -1}}},
		}
		for i, specs := range bad {
			if _, err := s.Update(specs); err == nil {
				t.Errorf("bad update %d succeeded", i)
			}
		}
		if len(s.List()) != 2 {
			t.Error("a failed update changed the sessions")
		}
		s.Close()
	})
}

func TestLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(time.Second))
		s := New(Config{Runner: r})
		t0 := time.Now()
		if err := s.Add(periodic("a", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.Add(periodic("a", 10*time.Second)); err == nil {
			t.Error("duplicate Add succeeded")
		}
		if err := s.Add(periodic("", 10*time.Second)); err == nil {
			t.Error("Add without a name succeeded")
		}
		time.Sleep(time.Second)
		if err := s.Stop("a"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(14 * time.Second)
		if err := s.Start("a"); err != nil {
			t.Fatal(err)
		}
		if err := s.Start("a"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		// Restarting starts the schedule over.
		if got := r.offsets("a", t0); !slices.Equal(got, secs(0, 15)) {
			t.Errorf("runs at %v", got)
		}
		for _, f := range []func(string) error{s.Start, s.Stop, s.Remove} {
			if err := f("nope"); !errors.Is(err, ErrNotFound) {
				t.Errorf("unknown session: %v", err)
			}
		}
		if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get unknown: %v", err)
		}
		if err := s.Remove("a"); err != nil || len(s.List()) != 0 {
			t.Errorf("Remove: %v, %d left", err, len(s.List()))
		}
		s.Close()
		if err := s.Add(periodic("b", time.Second)); !errors.Is(err, ErrClosed) {
			t.Errorf("Add after Close: %v", err)
		}
		if _, err := s.Update(nil); !errors.Is(err, ErrClosed) {
			t.Errorf("Update after Close: %v", err)
		}
		for _, f := range []func(string) error{s.Start, s.Stop, s.Remove} {
			if err := f("a"); !errors.Is(err, ErrClosed) {
				t.Errorf("after Close: %v, want ErrClosed", err)
			}
		}
	})
}

// TestReplace: Replace swaps a session's definition in one step, keeping
// its state, and leaves it alone on an invalid definition.
func TestReplace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(lasting(time.Second))
		s := New(Config{Runner: r})
		defer s.Close()
		if err := s.Add(periodic("a", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.Stop("a"); err != nil {
			t.Fatal(err)
		}
		if err := s.Replace(periodic("a", 20*time.Second)); err != nil {
			t.Fatal(err)
		}
		st, err := s.Get("a")
		if err != nil || st.State != StateStopped || st.Spec.Schedule.Frequency != 20*time.Second || st.Spec.Source != SourceAPI {
			t.Errorf("after Replace of a stopped session: %+v, %v", st, err)
		}
		if err := s.Start("a"); err != nil {
			t.Fatal(err)
		}
		if err := s.Replace(periodic("a", 0)); err == nil {
			t.Error("Replace with an invalid definition succeeded")
		}
		if st, _ := s.Get("a"); st.State != StateRunning || st.Spec.Schedule.Frequency != 20*time.Second {
			t.Errorf("after an invalid Replace: %+v", st)
		}
		if err := s.Replace(periodic("nope", time.Second)); !errors.Is(err, ErrNotFound) {
			t.Errorf("Replace of an unknown session: %v", err)
		}
		s.Close()
		if err := s.Replace(periodic("a", time.Second)); !errors.Is(err, ErrClosed) {
			t.Errorf("Replace after Close: %v", err)
		}
	})
}

func TestStateString(t *testing.T) {
	if StateRunning.String() != "running" || StateStopped.String() != "stopped" {
		t.Error("State.String")
	}
}

func TestLightRunner(t *testing.T) {
	_, err := LightRunner(nil).Run(t.Context(), &SessionSpec{}, sender.Hooks{})
	if se, ok := errors.AsType[*StageError](err); !ok || se.Stage != "connect" || errors.Unwrap(se) == nil {
		t.Fatalf("invalid config: %v", err)
	}

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	go echo(pc)
	cfg := sender.DefaultConfig(pc.LocalAddr().(*net.UDPAddr).AddrPort())
	cfg.Count, cfg.Interval, cfg.Timeout = 3, 5*time.Millisecond, time.Second
	var got []sender.Summary
	hooks := sender.Hooks{Summary: func(s sender.Summary) { got = append(got, s) }}
	clock := func() (bool, time.Duration) { return true, time.Millisecond }
	sum, err := LightRunner(clock).Run(t.Context(), &SessionSpec{Test: cfg}, hooks)
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil || sum.Received != 3 || len(got) != 1 {
		t.Errorf("run: %+v, %v", sum, err)
	}
}

// echo is a minimal stateful reflector.
func echo(pc net.PacketConn) {
	buf := make([]byte, 65536)
	var seq uint32
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		p := wire.ReflectorPacket{Seq: seq, ReceiveTimestamp: ntptime.FromTime(time.Now()), SenderTTL: 255}
		seq++
		m, err := p.Reflect(buf, n)
		if err != nil {
			continue
		}
		_ = wire.PutTimestamp(buf, ntptime.FromTime(time.Now()))
		_, _ = pc.WriteTo(buf[:m], from)
	}
}

// TestUpdateClashChangesNothing: a name clash with an API session is found
// before any configuration session is touched, whatever the map order.
func TestUpdateClashChangesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{Runner: newFakeRunner(lasting(time.Second))})
		var specs []SessionSpec
		for i := range 20 {
			specs = append(specs, periodic(string(rune('a'+i)), 10*time.Second))
		}
		if _, err := s.Update(specs); err != nil {
			t.Fatal(err)
		}
		if err := s.Add(periodic("api", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Update([]SessionSpec{periodic("api", time.Second)}); err == nil {
			t.Fatal("clashing update succeeded")
		}
		if n := len(s.List()); n != 21 {
			t.Errorf("%d sessions left, want 21", n)
		}
		s.Close()
	})
}

// TestFailureAfterSummary: a run whose runner fails after reporting its
// summary counts once, as failed, and its summary is marked invalid.
func TestFailureAfterSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newFakeRunner(func(_ context.Context, _ *SessionSpec, hooks sender.Hooks, _ int) (sender.Summary, error) {
			s := sender.Summary{EndedAt: time.Now(), SID: wire.SID{1, 2}}
			hooks.Summary(s)
			return s, &StageError{Stage: "stop", Err: errors.New("stop-sessions failed")}
		})
		sink := &recordSink{}
		s := New(Config{Runner: r, Sink: sink})
		if err := s.Add(periodic("a", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		st, _ := s.Get("a")
		sums := sink.of(store.EventRunSummary)
		if st.RunsOK != 0 || st.RunsFailed != 1 || len(sums) != 1 || !sums[0].Summary.Invalid || len(sums[0].SID) != 16 ||
			sink.count(store.EventRunError) != 1 {
			t.Errorf("status %+v, summaries %+v", st, sums)
		}
		s.Close()
	})
}

// TestUpdateStopsTogether: the sessions a reload removes or changes wind
// their runs down at once, not one after another (maintenance review A).
func TestUpdateStopsTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Each run takes a second to end once canceled, as a Full run
		// sending Stop-Sessions over a slow path might.
		r := newFakeRunner(func(ctx context.Context, _ *SessionSpec, _ sender.Hooks, _ int) (sender.Summary, error) {
			<-ctx.Done()
			time.Sleep(time.Second)
			return sender.Summary{}, ctx.Err()
		})
		s := New(Config{Runner: r})
		defer s.Close()
		specs := []SessionSpec{periodic("a", time.Hour), periodic("b", time.Hour), periodic("c", time.Hour)}
		if _, err := s.Update(specs); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		begin := time.Now()
		if _, err := s.Update(nil); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(begin); d != time.Second {
			t.Errorf("removing 3 sessions took %v, want 1s", d)
		}
	})
}

// TestRunLogLines: a periodic run logs two lines, its start and its end
// with the summary; a failure after the summary adds its own line.
func TestRunLogLines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		var failSecond atomic.Bool
		r := newFakeRunner(func(_ context.Context, _ *SessionSpec, hooks sender.Hooks, n int) (sender.Summary, error) {
			var s sender.Summary
			s.EndedAt, s.Sent, s.Received = time.Now(), 10, 10
			hooks.Summary(s)
			if n == 1 && failSecond.Load() {
				return s, &StageError{Stage: "stop", Err: errors.New("control connection lost")}
			}
			return s, nil
		})
		failSecond.Store(true)
		s := New(Config{Runner: r, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
		if err := s.Add(periodic("a", 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(15 * time.Second)
		synctest.Wait()
		s.Close()
		out := logs.String()
		if n := strings.Count(out, "msg=\"run started\""); n != 2 {
			t.Errorf("%d starts, want 2:\n%s", n, out)
		}
		if n := strings.Count(out, "msg=\"run ended\""); n != 1 || !strings.Contains(out, "sent=10") || strings.Contains(out, "msg=summary") {
			t.Errorf("want one end line carrying the summary:\n%s", out)
		}
		if !strings.Contains(out, "msg=\"run summary\"") || !strings.Contains(out, "msg=\"run failed\"") {
			t.Errorf("want the summary and the failure of the second run:\n%s", out)
		}
	})
}
