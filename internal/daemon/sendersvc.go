package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/ulid"
)

// senderService implements gotwamp.v1.SenderService: ad-hoc tests and the
// monitoring sessions.
type senderService struct {
	gotwampv1.UnimplementedSenderServiceServer
	// runCtx ends when the daemon stops; runs and watches end with it.
	runCtx context.Context //nolint:containedctx // the daemon's lifetime, not a request's
	cfg    *liveConfig
	runner *runner
	sched  *schedule.Scheduler // nil when the sender role is off
	store  store.Store
	// drop frees the results and the metrics of a deleted session.
	drop func(session string)
	log  *slog.Logger

	// maxTests and maxWatchers are daemon.limits.max_concurrent_tests and
	// max_watchers; watchers counts the open watches.
	maxTests, maxWatchers int
	watchers              atomic.Int64
	// deliveryWait bounds the delivery of a finished run's last events;
	// delivering counts the RPCs waiting on it, at most maxTests.
	deliveryWait time.Duration
	delivering   atomic.Int64

	mu   sync.Mutex
	runs map[string]context.CancelFunc // ad-hoc runs in progress by run ID
}

// newSenderService returns the SenderService of a daemon whose runs end
// with runCtx.
//
//declscope:package // Start registers it
func newSenderService(runCtx context.Context, cfg *liveConfig, r *runner, sched *schedule.Scheduler, st store.Store, drop func(string), log *slog.Logger) *senderService {
	limits := cfg.get().Daemon.Limits
	return &senderService{
		runCtx: runCtx, cfg: cfg, runner: r, sched: sched, store: st, drop: drop, log: log,
		maxTests: max(limits.MaxConcurrentTests, 1), maxWatchers: max(limits.MaxWatchers, 1),
		deliveryWait: finalDeliveryWait,
		runs:         map[string]context.CancelFunc{},
	}
}

// RunTest runs one ad-hoc test, Full or Light, and streams its events (SPEC
// §9.5). Invalid requests fail with a gRPC status; failures during the run
// are a RunError event.
func (s *senderService) RunTest(req *gotwampv1.RunTestRequest, stream gotwampv1.SenderService_RunTestServer) error {
	cfg := s.cfg.get()
	if !cfg.Sender.Enabled {
		return status.Error(codes.FailedPrecondition, "the sender role is disabled (sender.enabled: false)")
	}
	spec := req.GetSpec()
	plan, params, err := checkTestRequest(cfg, s.cfg.keys, spec)
	if err != nil {
		return err
	}
	target, ipv, source := spec.GetTarget(), ipvName(spec.GetIpv()), spec.GetSourceAddress()
	if plan.addr, err = s.runner.target(stream.Context(), target, ipv, plan.source); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	resolved := testSpecProto(target, ipv, source, params)

	// The run ends when the client goes away, StopTest is called or the
	// daemon stops.
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	defer context.AfterFunc(s.runCtx, cancel)()
	runID := ulid.New(time.Now(), nil)
	s.mu.Lock()
	if len(s.runs) >= s.maxTests {
		s.mu.Unlock()
		return status.Errorf(codes.ResourceExhausted, "%d ad-hoc tests are running (daemon.limits.max_concurrent_tests)", s.maxTests)
	}
	s.runs[runID] = cancel
	s.mu.Unlock()

	// The run goes on its own goroutines and only queues its events; this
	// handler alone sends them on the stream.
	q := newEventQueue(runID)
	var sid []byte
	hooks := sender.Hooks{
		Packet: func(r sender.PacketResult) {
			if ev := packetEvent(r); ev != nil {
				q.offer(ev)
			}
		},
		Lost: func(l sender.LostPacket) {
			q.offer(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Lost{Lost: &gotwampv1.PacketLost{Seq: l.Seq, T1: timestamppb.New(l.T1)}}})
		},
		Summary: func(sum sender.Summary) {
			q.put(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Summary{Summary: runSummary(sum, "", runID, resolved, sid, gotwampv1.SummaryKind_SUMMARY_KIND_RUN)}})
		},
	}
	started := func(local, remote netip.AddrPort, id []byte) {
		sid = id
		q.put(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Started{Started: &gotwampv1.RunStarted{
			Spec: resolved, Local: protoEndpoint(local), Remote: protoEndpoint(remote), Sid: id, StartedAt: timestamppb.Now(),
		}}})
	}

	log := s.log.With("run_id", runID, "target", target, "mode", params.Mode)
	log.Info("ad-hoc test started", "count", params.Count)
	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		sum, err := recovered(func() (sender.Summary, error) {
			return s.runner.run(ctx, &plan, plan.test, hooks, started, nil)
		})
		// The slot is free once the run is over, even while a client that
		// stopped reading holds up the delivery of its last events.
		s.mu.Lock()
		delete(s.runs, runID)
		s.mu.Unlock()
		log.Info("ad-hoc test ended", "remote", sum.Remote, "sid", hex.EncodeToString(sid), "sent", sum.Sent,
			"received", sum.Received, "lost", sum.Lost, "aborted", sum.Aborted, "error", err)
		done <- outcome{err}
	}()

	// One goroutine sends. A Send to a client that does not read returns
	// only when the RPC ends, that is when this handler returns, so the
	// handler must not wait on it without a limit.
	finished := make(chan struct{}) // the run is over and its last events are queued
	sent := make(chan error, 1)     // the sender's outcome once it has sent them
	go func() {
		var sendErr error
		for {
			last := false
			select {
			case <-q.ready():
			case <-finished:
				last = true
			}
			for _, ev := range q.take() {
				if sendErr != nil {
					continue // the client is gone: discard
				}
				if sendErr = stream.Send(ev); sendErr != nil {
					cancel() // stop the run; nobody reads its results
				}
			}
			if last {
				sent <- sendErr
				return
			}
		}
	}()

	o := <-done
	var se *schedule.StageError
	switch err := o.err; {
	case err == nil:
	case s.runCtx.Err() != nil:
		q.put(runError("test", "gotwampd is shutting down", 0))
	case errors.Is(err, context.Canceled) && stream.Context().Err() == nil:
		// StopTest: the aborted summary has been queued.
	case errors.Is(err, context.Canceled):
		// The client went away; there is no one to tell.
	case errors.As(err, &se):
		q.put(runError(se.Stage, se.Err.Error(), se.Accept))
	default:
		q.put(runError("test", err.Error(), 0))
	}
	close(finished)
	return s.deliver(sent, log)
}

// finalDeliveryWait is how long a finished ad-hoc run's last events may take
// to reach a client that has stopped reading.
const finalDeliveryWait = 5 * time.Second

// deliver waits for the sender of a finished run to deliver its last
// events. A client that does not take them within deliveryWait, or one
// beyond the maxTests RPCs already waiting so, has its RPC ended, which
// releases the blocked Send and everything the RPC holds (Codex M1).
func (s *senderService) deliver(sent <-chan error, log *slog.Logger) error {
	select {
	case err := <-sent:
		return err
	default:
	}
	if n := s.delivering.Add(1); n > int64(s.maxTests) {
		s.delivering.Add(-1)
		log.Warn("too many finished tests wait for their clients to read; ending this one", "waiting", n-1)
		return status.Errorf(codes.ResourceExhausted, "%d finished tests wait for their clients to read the results", n-1)
	}
	defer s.delivering.Add(-1)
	t := time.NewTimer(s.deliveryWait)
	defer t.Stop()
	select {
	case err := <-sent:
		return err
	case <-t.C:
		log.Warn("the client did not read the last results in time; ending the RPC", "wait", s.deliveryWait)
		return status.Errorf(codes.DeadlineExceeded, "the results were not read within %v of the end of the test", s.deliveryWait)
	}
}

// mergeWatchers returns the events of every watcher on one channel, closed
// once every watcher's is. A single watcher's channel is returned as is.
func mergeWatchers(ctx context.Context, watchers []*store.Watcher) <-chan store.Event {
	if len(watchers) == 1 {
		return watchers[0].C()
	}
	out := make(chan store.Event)
	var wg sync.WaitGroup
	for _, w := range watchers {
		wg.Go(func() {
			for e := range w.C() {
				select {
				case out <- e:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// recovered runs f; a panic in it fails the run instead of the daemon
// (SPEC §12.2), as the scheduler does for monitoring sessions.
func recovered(f func() (sender.Summary, error)) (sum sender.Summary, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &schedule.StageError{Stage: "internal", Err: fmt.Errorf("panic: %v", v)}
		}
	}()
	return f()
}

// StopTest ends an ad-hoc test; its stream then delivers the aborted summary.
func (s *senderService) StopTest(_ context.Context, req *gotwampv1.StopTestRequest) (*gotwampv1.StopTestResponse, error) {
	if err := checkRunID(req.GetRunId()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cancel, ok := s.runs[req.GetRunId()]
	s.mu.Unlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no ad-hoc test %q is running", req.GetRunId())
	}
	cancel()
	return &gotwampv1.StopTestResponse{}, nil
}

// scheduler returns the scheduler, or an error when the sender role is off.
func (s *senderService) scheduler() (*schedule.Scheduler, error) {
	if s.sched == nil {
		return nil, status.Error(codes.FailedPrecondition, "the sender role is disabled (sender.enabled: false)")
	}
	return s.sched, nil
}

// schedError converts a scheduler error.
func schedError(name string, err error) error {
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		return status.Errorf(codes.NotFound, "no session %q", name)
	case errors.Is(err, schedule.ErrClosed):
		// Not Unavailable: the CLI reads that as "cannot connect" (exit 3),
		// but the daemon answered (B5).
		return status.Error(codes.Aborted, "gotwampd is shutting down")
	default:
		return status.Error(codes.FailedPrecondition, err.Error())
	}
}

// sessionView builds the API view of a session.
func (s *senderService) sessionView(st schedule.Status) *gotwampv1.Session {
	opts, _ := st.Spec.Options.(sessionOptions)
	v := &gotwampv1.Session{
		Spec:        sessionSpecProto(opts.def),
		State:       gotwampv1.SessionState_SESSION_STATE_RUNNING,
		Source:      gotwampv1.SessionSource_SESSION_SOURCE_CONFIG,
		Running:     st.Running,
		RunsOk:      st.RunsOK,
		RunsFailed:  st.RunsFailed,
		RunsSkipped: st.Skipped,
		LastRunAt:   optTimestamp(st.LastRun),
	}
	if st.State == schedule.StateStopped {
		v.State = gotwampv1.SessionState_SESSION_STATE_STOPPED
	}
	if st.Spec.Source == schedule.SourceAPI {
		v.Source = gotwampv1.SessionSource_SESSION_SOURCE_API
	}
	events := s.store.Recent(st.Spec.Name, 0)
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == store.EventRunError && v.LastSummary == nil && v.LastError == nil {
			v.LastError = runError(e.Error.Stage, e.Error.Message, e.Error.Accept).GetError()
			continue
		}
		if e.Kind == store.EventRunSummary || e.Kind == store.EventWindowSummary {
			v.LastSummary = storeEvent(e).GetSummary()
			break
		}
	}
	return v
}

func (s *senderService) ListSessions(context.Context, *gotwampv1.ListSessionsRequest) (*gotwampv1.ListSessionsResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	resp := &gotwampv1.ListSessionsResponse{}
	for _, st := range sched.List() {
		resp.Sessions = append(resp.Sessions, s.sessionView(st))
	}
	return resp, nil
}

func (s *senderService) GetSession(_ context.Context, req *gotwampv1.GetSessionRequest) (*gotwampv1.GetSessionResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	if err := checkName(req.GetName()); err != nil {
		return nil, err
	}
	st, err := sched.Get(req.GetName())
	if err != nil {
		return nil, schedError(req.GetName(), err)
	}
	return &gotwampv1.GetSessionResponse{Session: s.sessionView(st)}, nil
}

// apiSession reads and validates a session definition of the API.
func (s *senderService) apiSession(ps *gotwampv1.SessionSpec) (schedule.SessionSpec, error) {
	return checkSessionRequest(s.cfg.get(), s.cfg.keys, ps)
}

// apiOwned fails for a session that does not exist or is not the API's.
func apiOwned(sched *schedule.Scheduler, name string) (schedule.Status, error) {
	st, err := sched.Get(name)
	if err != nil {
		return st, schedError(name, err)
	}
	if st.Spec.Source != schedule.SourceAPI {
		return st, status.Errorf(codes.FailedPrecondition, "session %q is defined in the configuration file; edit it there and reload", name)
	}
	return st, nil
}

func (s *senderService) CreateSession(_ context.Context, req *gotwampv1.CreateSessionRequest) (*gotwampv1.CreateSessionResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	spec, err := s.apiSession(req.GetSpec())
	if err != nil {
		return nil, err
	}
	if _, err := sched.Get(spec.Name); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "session %q exists", spec.Name)
	}
	if err := sched.Add(spec); err != nil {
		if _, gerr := sched.Get(spec.Name); gerr == nil {
			// Created meanwhile by a concurrent request.
			return nil, status.Errorf(codes.AlreadyExists, "session %q exists", spec.Name)
		}
		return nil, schedError(spec.Name, err)
	}
	s.log.Info("session created", "session", spec.Name)
	st, err := sched.Get(spec.Name)
	if err != nil {
		return nil, schedError(spec.Name, err)
	}
	return &gotwampv1.CreateSessionResponse{Session: s.sessionView(st)}, nil
}

func (s *senderService) UpdateSession(_ context.Context, req *gotwampv1.UpdateSessionRequest) (*gotwampv1.UpdateSessionResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	spec, err := s.apiSession(req.GetSpec())
	if err != nil {
		return nil, err
	}
	if _, err := apiOwned(sched, spec.Name); err != nil {
		return nil, err
	}
	// One scheduler operation: a stopped session stays stopped, and nothing
	// can take the name between the old definition and the new.
	if err := sched.Replace(spec); err != nil {
		return nil, schedError(spec.Name, err)
	}
	s.log.Info("session updated", "session", spec.Name)
	st, err := sched.Get(spec.Name)
	if err != nil {
		return nil, schedError(spec.Name, err)
	}
	return &gotwampv1.UpdateSessionResponse{Session: s.sessionView(st)}, nil
}

func (s *senderService) DeleteSession(_ context.Context, req *gotwampv1.DeleteSessionRequest) (*gotwampv1.DeleteSessionResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if err := checkName(name); err != nil {
		return nil, err
	}
	if _, err := apiOwned(sched, name); err != nil {
		return nil, err
	}
	if err := sched.Remove(name); err != nil {
		return nil, schedError(name, err)
	}
	s.drop(name)
	s.log.Info("session deleted", "session", name)
	return &gotwampv1.DeleteSessionResponse{}, nil
}

func (s *senderService) StartSession(_ context.Context, req *gotwampv1.StartSessionRequest) (*gotwampv1.StartSessionResponse, error) {
	v, err := s.setState(req.GetName(), (*schedule.Scheduler).Start)
	if err != nil {
		return nil, err
	}
	return &gotwampv1.StartSessionResponse{Session: v}, nil
}

func (s *senderService) StopSession(_ context.Context, req *gotwampv1.StopSessionRequest) (*gotwampv1.StopSessionResponse, error) {
	v, err := s.setState(req.GetName(), (*schedule.Scheduler).Stop)
	if err != nil {
		return nil, err
	}
	return &gotwampv1.StopSessionResponse{Session: v}, nil
}

func (s *senderService) setState(name string, op func(*schedule.Scheduler, string) error) (*gotwampv1.Session, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	if err := op(sched, name); err != nil {
		return nil, schedError(name, err)
	}
	st, err := sched.Get(name)
	if err != nil {
		return nil, schedError(name, err)
	}
	return s.sessionView(st), nil
}

func (s *senderService) GetSessionStats(_ context.Context, req *gotwampv1.GetSessionStatsRequest) (*gotwampv1.GetSessionStatsResponse, error) {
	sched, err := s.scheduler()
	if err != nil {
		return nil, err
	}
	name := req.GetName()
	if err := checkName(name); err != nil {
		return nil, err
	}
	if err := checkSince(req.GetSince()); err != nil {
		return nil, err
	}
	if _, err := sched.Get(name); err != nil && !slices.Contains(s.store.Sessions(), name) {
		return nil, schedError(name, err)
	}
	resp := &gotwampv1.GetSessionStatsResponse{}
	for _, e := range s.statsEvents(name, req.GetSince()) {
		if e.Kind != store.EventRunSummary && e.Kind != store.EventWindowSummary {
			continue
		}
		resp.Summaries = append(resp.Summaries, storeEvent(e).GetSummary())
	}
	if n := int(req.GetLast()); n > 0 && n < len(resp.Summaries) {
		resp.Summaries = resp.Summaries[len(resp.Summaries)-n:]
	}
	return resp, nil
}

// rangeEnd is the end of a time range with none: the last time a Unix
// nanosecond count holds.
var rangeEnd = time.Unix(0, math.MaxInt64)

// statsEvents returns the results of GetSessionStats: those since since
// through the store's time range, which a database store answers from the
// file, as far back as its retention, and the memory store from its
// history; or, without since, the history in memory.
func (s *senderService) statsEvents(name string, since *timestamppb.Timestamp) []store.Event {
	if since == nil {
		return s.store.Recent(name, 0)
	}
	from := since.AsTime()
	if from.Before(time.Unix(0, 0)) {
		from = time.Unix(0, 0) // before 1970: no Unix nanosecond count
	}
	return s.store.Range(name, from, rangeEnd)
}

// WatchResults streams the results of the sessions asked for as they come.
// A subscriber too slow to keep up loses events and is told how many.
func (s *senderService) WatchResults(req *gotwampv1.WatchResultsRequest, stream gotwampv1.SenderService_WatchResultsServer) error {
	if _, err := s.scheduler(); err != nil {
		return err
	}
	if err := checkWatch(req); err != nil {
		return err
	}
	if n := s.watchers.Add(1); n > int64(s.maxWatchers) {
		s.watchers.Add(-1)
		return status.Errorf(codes.ResourceExhausted, "%d watches are open (daemon.limits.max_watchers)", s.maxWatchers)
	}
	defer s.watchers.Add(-1)
	// One subscription per session asked for, so that the events of the
	// others neither fill its buffer nor count as dropped (B31).
	names := req.GetSessions()
	var watchers []*store.Watcher
	defer func() {
		for _, w := range watchers {
			w.Close()
		}
	}()
	if len(names) == 0 {
		watchers = append(watchers, s.store.Watch(store.WatchOptions{Packets: req.GetPackets()}))
	}
	seen := map[string]bool{}
	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			watchers = append(watchers, s.store.Watch(store.WatchOptions{Session: name, Packets: req.GetPackets()}))
		}
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	events := mergeWatchers(ctx, watchers)
	var dropped uint64
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-s.runCtx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			if e.Session == nil || e.Session.Name == "" {
				continue
			}
			var d uint64
			for _, w := range watchers {
				d += w.Dropped()
			}
			if d > dropped {
				if err := stream.Send(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Dropped{Dropped: &gotwampv1.EventsDropped{Count: d - dropped}}}); err != nil {
					return err
				}
				dropped = d
			}
			// Nothing of the event outlives its delivery: a watch keeps no
			// session definition, deleted or not (Codex M8).
			if ev := storeEvent(e); ev != nil {
				if err := stream.Send(ev); err != nil {
					return err
				}
			}
		}
	}
}
