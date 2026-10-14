package cli

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// fakeDaemon serves scripted answers in process.
//
//declscope:package // the fixture of every cli test file
type fakeDaemon struct {
	gotwampv1.UnimplementedDaemonServiceServer
	gotwampv1.UnimplementedSenderServiceServer
	gotwampv1.UnimplementedReflectorServiceServer

	events []*gotwampv1.TestEvent
	runErr error
	// block makes RunTest wait for the client to go away after its events.
	block bool
	// pause delays the events after the first one.
	pause time.Duration
	// stopEvents, when set, are sent once StopTest is called, after which
	// RunTest returns; ignoreStop makes StopTest succeed without effect.
	stopEvents []*gotwampv1.TestEvent
	ignoreStop bool
	// watch are the events WatchResults streams before it returns.
	watch []*gotwampv1.TestEvent
	// stop is signaled by StopTest.
	//
	//declscope:private
	stop chan struct{}
	// requests are the last request of each RPC, by method name.
	//
	//declscope:private
	requests map[string]proto.Message
	//declscope:private
	mu sync.Mutex
	// spec is the last RunTest request.
	//
	//declscope:private
	spec *gotwampv1.TestSpec
}

func (f *fakeDaemon) GetStatus(context.Context, *gotwampv1.GetStatusRequest) (*gotwampv1.GetStatusResponse, error) {
	return &gotwampv1.GetStatusResponse{
		Version: "v0.2.0", Commit: "abc1234", GoVersion: "go1.26.7",
		Uptime:           durationpb.New(90 * time.Minute),
		ReflectorEnabled: true,
		TimestampingMode: gotwampv1.TimestampingMode_TIMESTAMPING_MODE_USERSPACE,
		Clock:            &gotwampv1.ClockStatus{Synchronized: true, MaxError: durationpb.New(250 * time.Microsecond), Source: gotwampv1.ClockSyncSource_CLOCK_SYNC_SOURCE_KERNEL},
		Store:            &gotwampv1.StoreStatus{Backend: "sqlite", Path: "/var/lib/gotwamp/results.db", ResultsWritten: 12, ResultsDropped: 1},
	}, nil
}

func (f *fakeDaemon) RunTest(req *gotwampv1.RunTestRequest, stream gotwampv1.SenderService_RunTestServer) error {
	f.mu.Lock()
	f.spec = req.GetSpec()
	events, runErr, block := f.events, f.runErr, f.block
	f.mu.Unlock()
	if runErr != nil {
		return runErr
	}
	for i, ev := range events {
		if i == 1 && f.pause > 0 {
			select {
			case <-time.After(f.pause):
			case <-stream.Context().Done():
				return stream.Context().Err()
			}
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	if f.stopEvents != nil || f.ignoreStop {
		select {
		case <-f.stop:
			for _, ev := range f.stopEvents {
				if err := stream.Send(ev); err != nil {
					return err
				}
			}
			if !f.ignoreStop {
				return nil
			}
		case <-stream.Context().Done():
			return nil
		}
		<-stream.Context().Done()
		return nil
	}
	if block {
		<-stream.Context().Done()
	}
	return nil
}

func (f *fakeDaemon) record(name string, req proto.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests[name] = req
}

// last returns the last request of an RPC, or nil.
//
//declscope:package // the fixture of every cli test file
func (f *fakeDaemon) last(name string) proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[name]
}

func (f *fakeDaemon) StopTest(_ context.Context, req *gotwampv1.StopTestRequest) (*gotwampv1.StopTestResponse, error) {
	f.record("StopTest", req)
	select {
	case f.stop <- struct{}{}:
	default:
	}
	return &gotwampv1.StopTestResponse{}, nil
}

// fakeSession is the session the fake reports for spec.
func fakeSession(spec *gotwampv1.SessionSpec) *gotwampv1.Session {
	return &gotwampv1.Session{
		Spec: spec, State: gotwampv1.SessionState_SESSION_STATE_RUNNING, Source: gotwampv1.SessionSource_SESSION_SOURCE_API,
		RunsOk: 3, LastSummary: &gotwampv1.RunSummary{Sent: 10, Received: 10, Rtt: &gotwampv1.DelayStats{Samples: 10, Median: durationpb.New(2 * time.Millisecond)}},
	}
}

func fakeSpec(name string) *gotwampv1.SessionSpec {
	return &gotwampv1.SessionSpec{
		Name:     name,
		Spec:     &gotwampv1.TestSpec{Target: "192.0.2.10", Mode: gotwampv1.Mode_MODE_FULL, Count: proto.Uint32(100), Interval: durationpb.New(100 * time.Millisecond)},
		Schedule: &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC, Frequency: durationpb.New(time.Minute)},
		Labels:   map[string]string{"site": "tokyo"},
	}
}

func (f *fakeDaemon) ListSessions(_ context.Context, req *gotwampv1.ListSessionsRequest) (*gotwampv1.ListSessionsResponse, error) {
	f.record("ListSessions", req)
	failed := fakeSession(fakeSpec("osaka"))
	failed.LastError = &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_REQUEST, Message: "accept-session accept=5"}
	return &gotwampv1.ListSessionsResponse{Sessions: []*gotwampv1.Session{fakeSession(fakeSpec("tokyo")), failed}}, nil
}

func (f *fakeDaemon) GetSession(_ context.Context, req *gotwampv1.GetSessionRequest) (*gotwampv1.GetSessionResponse, error) {
	f.record("GetSession", req)
	if req.GetName() != "tokyo" {
		return nil, status.Error(codes.NotFound, "no session")
	}
	return &gotwampv1.GetSessionResponse{Session: fakeSession(fakeSpec("tokyo"))}, nil
}

func (f *fakeDaemon) CreateSession(_ context.Context, req *gotwampv1.CreateSessionRequest) (*gotwampv1.CreateSessionResponse, error) {
	f.record("CreateSession", req)
	return &gotwampv1.CreateSessionResponse{Session: fakeSession(req.GetSpec())}, nil
}

func (f *fakeDaemon) UpdateSession(_ context.Context, req *gotwampv1.UpdateSessionRequest) (*gotwampv1.UpdateSessionResponse, error) {
	f.record("UpdateSession", req)
	return &gotwampv1.UpdateSessionResponse{Session: fakeSession(req.GetSpec())}, nil
}

func (f *fakeDaemon) DeleteSession(_ context.Context, req *gotwampv1.DeleteSessionRequest) (*gotwampv1.DeleteSessionResponse, error) {
	f.record("DeleteSession", req)
	return &gotwampv1.DeleteSessionResponse{}, nil
}

func (f *fakeDaemon) StartSession(_ context.Context, req *gotwampv1.StartSessionRequest) (*gotwampv1.StartSessionResponse, error) {
	f.record("StartSession", req)
	return &gotwampv1.StartSessionResponse{Session: fakeSession(fakeSpec(req.GetName()))}, nil
}

func (f *fakeDaemon) StopSession(_ context.Context, req *gotwampv1.StopSessionRequest) (*gotwampv1.StopSessionResponse, error) {
	f.record("StopSession", req)
	s := fakeSession(fakeSpec(req.GetName()))
	s.State = gotwampv1.SessionState_SESSION_STATE_STOPPED
	return &gotwampv1.StopSessionResponse{Session: s}, nil
}

func (f *fakeDaemon) GetSessionStats(_ context.Context, req *gotwampv1.GetSessionStatsRequest) (*gotwampv1.GetSessionStatsResponse, error) {
	f.record("GetSessionStats", req)
	sum := func(lost uint32) *gotwampv1.RunSummary {
		return &gotwampv1.RunSummary{
			EndedAt: timestamppb.New(time.Date(2026, 10, 4, 12, 0, 0, 0, time.Local)), Sent: 100, Received: 100 - lost, Lost: lost,
			LossRatio: float64(lost) / 100,
			Rtt:       &gotwampv1.DelayStats{Samples: 1, Min: durationpb.New(time.Millisecond), Median: durationpb.New(2 * time.Millisecond), Max: durationpb.New(3 * time.Millisecond)},
		}
	}
	return &gotwampv1.GetSessionStatsResponse{Summaries: []*gotwampv1.RunSummary{sum(0), sum(5)}}, nil
}

func (f *fakeDaemon) WatchResults(req *gotwampv1.WatchResultsRequest, stream gotwampv1.SenderService_WatchResultsServer) error {
	f.record("WatchResults", req)
	for _, ev := range f.watch {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeDaemon) ReloadConfig(context.Context, *gotwampv1.ReloadConfigRequest) (*gotwampv1.ReloadConfigResponse, error) {
	return &gotwampv1.ReloadConfigResponse{SessionsAdded: []string{"new"}, SessionsUnchanged: 2, Warnings: []string{"restart gotwampd to apply changes of reflector"}}, nil
}

func (f *fakeDaemon) GetReflectorStatus(context.Context, *gotwampv1.GetReflectorStatusRequest) (*gotwampv1.GetReflectorStatusResponse, error) {
	return &gotwampv1.GetReflectorStatusResponse{
		Enabled: true,
		Listeners: []*gotwampv1.ReflectorListener{
			{Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_LIGHT, Address: "0.0.0.0:862", PacketsReceived: 7, PacketsReflected: 6},
			{Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL, Address: "0.0.0.0:862"},
		},
		ControlConnectionsActive: 1, ControlConnectionsAccepted: 4, FullSessionsActive: 1, LightFlowsActive: 2,
		Counters: []*gotwampv1.ReflectorCounters{{
			Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_LIGHT, PacketsReceived: 7, PacketsReflected: 6,
			Dropped: []*gotwampv1.DropCount{{Reason: gotwampv1.DropReason_DROP_REASON_ACL, Count: 1}},
		}},
	}, nil
}

func (f *fakeDaemon) ListReflectorSessions(context.Context, *gotwampv1.ListReflectorSessionsRequest) (*gotwampv1.ListReflectorSessionsResponse, error) {
	ep := func(a string, p uint32) *gotwampv1.Endpoint { return &gotwampv1.Endpoint{Address: a, Port: p} }
	return &gotwampv1.ListReflectorSessionsResponse{
		FullSessions: []*gotwampv1.FullReflectorSession{{
			Sid: []byte{0xc0, 0, 2, 5, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, ControlPeer: ep("192.0.2.1", 40000), Sender: ep("192.0.2.1", 50000),
			Reflector: ep("192.0.2.5", 50000), State: gotwampv1.FullSessionState_FULL_SESSION_STATE_ACTIVE, PacketsReceived: 3, PacketsReflected: 3,
			Security: gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED,
		}},
		LightFlows: []*gotwampv1.LightFlow{{Source: ep("2001:db8::1", 64374), Destination: ep("2001:db8::5", 862), NextSeq: 9, PacketsReceived: 9, PacketsReflected: 9, LastReceivedAt: timestamppb.Now()}},
	}, nil
}

// lastSpec returns the spec of the last RunTest.
//
//declscope:package // the fixture of every cli test file
func (f *fakeDaemon) lastSpec() *gotwampv1.TestSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spec
}

// harness runs gotwamp against a fakeDaemon over bufconn.
//
//declscope:package // the fixture of every cli test file
type harness struct {
	fake *fakeDaemon
	//declscope:private
	dial func(string) (*grpc.ClientConn, error)
}

// newFakeDaemon returns a fake daemon with nothing scripted.
//
//declscope:package // the fixture of every cli test file
func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{stop: make(chan struct{}, 1), requests: map[string]proto.Message{}}
}

// newHarness serves a fakeDaemon until the test ends.
//
//declscope:package // the fixture of every cli test file
func newHarness(t *testing.T) *harness {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	fake := newFakeDaemon()
	gotwampv1.RegisterDaemonServiceServer(srv, fake)
	gotwampv1.RegisterSenderServiceServer(srv, fake)
	gotwampv1.RegisterReflectorServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return &harness{fake: fake, dial: func(string) (*grpc.ClientConn, error) {
		return grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
	}}
}

// run runs gotwamp with args.
//
//declscope:package // the fixture of every cli test file
func (h *harness) run(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	return h.runInterrupted(ctx, nil, args...)
}

// runInterrupted runs gotwamp with args and interrupts as Env.Interrupt.
//
//declscope:package // the fixture of every cli test file
func (h *harness) runInterrupted(ctx context.Context, interrupts <-chan struct{}, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Main(ctx, args, BuildInfo{Version: "v0.2.0", Commit: "abc1234"}, Env{
		Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }, Dial: h.dial, Interrupt: interrupts,
	})
	return code, out.String(), errb.String()
}
