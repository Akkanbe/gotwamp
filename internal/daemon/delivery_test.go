//declscope:namespace sendersvc // tests of the RunTest delivery and the watch's memory

package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// blockedStream is a RunTest stream whose client never reads: Send waits
// until the RPC ends, as gRPC's does once flow control is exhausted.
type blockedStream struct {
	grpc.ServerStream
	ctx context.Context //nolint:containedctx // the RPC's
	end context.CancelFunc
}

func newBlockedStream(t *testing.T) *blockedStream {
	ctx, end := context.WithCancel(t.Context())
	return &blockedStream{ctx: ctx, end: end}
}

func (b *blockedStream) Context() context.Context { return b.ctx }

func (b *blockedStream) Send(*gotwampv1.TestEvent) error {
	<-b.ctx.Done()
	return b.ctx.Err()
}

// recordingStream is a WatchResults stream that hands every event on.
type recordingStream struct {
	grpc.ServerStream
	ctx    context.Context //nolint:containedctx // the RPC's
	events chan *gotwampv1.TestEvent
}

func (r *recordingStream) Context() context.Context { return r.ctx }

func (r *recordingStream) Send(ev *gotwampv1.TestEvent) error {
	r.events <- ev
	return nil
}

// testService is a SenderService outside any gRPC server, whose handlers
// the tests call directly, with max_concurrent_tests at 1.
func testService(t *testing.T) (*senderService, *store.Memory) {
	t.Helper()
	c := testConfig(t)
	c.Daemon.Limits.MaxConcurrentTests = 1
	live := &liveConfig{}
	live.p.Store(c)
	memory := store.NewMemory(10, 10)
	run := newRunner(func() (bool, time.Duration) { return true, time.Millisecond }, newSIDSink(memory), nil)
	sched := schedule.New(schedule.Config{Runner: run, Sink: memory, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(sched.Close)
	return newSenderService(t.Context(), live, run, sched, memory, memory.Delete, slog.New(slog.DiscardHandler)), memory
}

// runID waits for the one ad-hoc run of s and returns its ID.
func (s *senderService) runID(t *testing.T) string {
	t.Helper()
	var id string
	waitFor(t, "the run to start", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for k := range s.runs {
			id = k
		}
		return id != ""
	})
	return id
}

// TestStoppedHandlersEnd: RunTest, never read, StopTest, again and again:
// each RPC ends within the delivery wait, so neither handlers nor their
// senders pile up beyond max_concurrent_tests (Codex re-review M1).
func TestStoppedHandlersEnd(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	s, _ := testService(t)
	s.deliveryWait = 200 * time.Millisecond
	baseline := runtime.NumGoroutine()
	req := &gotwampv1.RunTestRequest{Spec: lightSpec(r.d.LightAddrs()[0], 100_000)}
	for i := range 4 {
		st := newBlockedStream(t)
		errc := make(chan error, 1)
		go func() {
			err := s.RunTest(req, st)
			st.end() // gRPC ends the RPC when its handler returns
			errc <- err
		}()
		if _, err := s.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: s.runID(t)}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-errc:
			if status.Code(err) != codes.DeadlineExceeded {
				t.Errorf("run %d: RunTest = %v, want DeadlineExceeded", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("run %d: the stopped RPC did not end", i)
		}
	}
	if n := s.delivering.Load(); n != 0 {
		t.Errorf("%d RPCs still wait to deliver", n)
	}
	waitFor(t, "the goroutines of the RPCs to end", func() bool { return runtime.NumGoroutine() <= baseline+2 })
}

// TestDeliveryCap: beyond max_concurrent_tests finished runs waiting for
// their clients, the next one is ended at once.
func TestDeliveryCap(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	s, _ := testService(t)
	s.deliveryWait = time.Hour
	req := &gotwampv1.RunTestRequest{Spec: lightSpec(r.d.LightAddrs()[0], 100_000)}
	first := newBlockedStream(t)
	firstErr := make(chan error, 1)
	go func() { firstErr <- s.RunTest(req, first) }()
	if _, err := s.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: s.runID(t)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first RPC to wait for its client", func() bool { return s.delivering.Load() == 1 })

	second := newBlockedStream(t)
	defer second.end()
	secondErr := make(chan error, 1)
	go func() { secondErr <- s.RunTest(req, second) }()
	if _, err := s.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: s.runID(t)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondErr:
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("second RunTest = %v, want ResourceExhausted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second stopped RPC did not end")
	}
	first.end() // the client goes away: its RPC ends too
	if err := <-firstErr; err == nil {
		t.Error("first RunTest ended without an error")
	}
}

// TestWatchKeepsNoDefinitions: a watch that sees generation after
// generation of a session, each deleted after its result, keeps none of
// them (Codex re-review M8).
func TestWatchKeepsNoDefinitions(t *testing.T) {
	s, memory := testService(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ws := &recordingStream{ctx: ctx, events: make(chan *gotwampv1.TestEvent, 1)}
	done := make(chan error, 1)
	go func() { done <- s.WatchResults(&gotwampv1.WatchResultsRequest{Sessions: []string{"gen"}}, ws) }()

	const generations = 100
	var collected atomic.Int64
	for i := range generations {
		def := config.Session{Name: "gen", Target: fmt.Sprintf("192.0.2.%d", i%250+1), TestParams: config.Default().Sender.Defaults}
		info := &store.SessionInfo{Name: "gen", Definition: def}
		runtime.AddCleanup(info, func(int) { collected.Add(1) }, 0)
		e := store.Event{Kind: store.EventRunSummary, Time: time.Now(), Session: info, RunID: "r", Summary: &sender.Summary{}}
		// The watch may not be subscribed yet when the first one goes out.
		var got *gotwampv1.TestEvent
		for got == nil {
			memory.Publish(e)
			select {
			case got = <-ws.events:
			case <-time.After(100 * time.Millisecond):
			}
		}
		if got.GetSummary().GetSpec().GetTarget() != def.Target {
			t.Fatalf("generation %d: spec %v", i, got.GetSummary().GetSpec())
		}
		memory.Delete("gen")
	}
	waitFor(t, "the deleted generations to be collected", func() bool {
		runtime.GC()
		return collected.Load() >= generations-1 // the latest may still be in flight
	})
	cancel()
	<-done
}
