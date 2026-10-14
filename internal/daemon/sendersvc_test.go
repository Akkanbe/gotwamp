package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// lightSpec is a Light test of count packets to target.
//
//declscope:package // the RunTest tests of several files build their requests with it
func lightSpec(target netip.AddrPort, count uint32) *gotwampv1.TestSpec {
	return &gotwampv1.TestSpec{
		Target:       target.Addr().String(),
		Mode:         gotwampv1.Mode_MODE_LIGHT,
		ReceiverPort: proto.Uint32(uint32(target.Port())),
		Count:        proto.Uint32(count),
		Interval:     durationpb.New(10 * time.Millisecond),
		Timeout:      durationpb.New(time.Second),
	}
}

// collect reads a RunTest stream to its end.
//
//declscope:package // the RunTest tests of several files read their streams with it
func collect(t *testing.T, stream gotwampv1.SenderService_RunTestClient) []*gotwampv1.TestEvent {
	t.Helper()
	var evs []*gotwampv1.TestEvent
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return evs
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		evs = append(evs, ev)
	}
}

// TestRunTestLight measures the daemon's own Light reflector over the
// loopback: the M2 end-to-end path.
func TestRunTestLight(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	target := r.d.LightAddrs()[0]

	spec := lightSpec(target, 5)
	spec.Dscp = proto.Uint32(46)
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	if len(evs) != 7 {
		t.Fatalf("%d events, want started + 5 packets + summary: %v", len(evs), evs)
	}
	runID := evs[0].GetRunId()
	if len(runID) != 26 {
		t.Errorf("run id %q", runID)
	}
	started := evs[0].GetStarted()
	// The wildcard-bound socket is shown with the route's source address.
	if started == nil || started.GetRemote().GetPort() != uint32(target.Port()) || started.GetLocal().GetAddress() != "127.0.0.1" ||
		started.GetLocal().GetPort() < 49152 || started.GetLocal().GetPort() > 57343 || started.GetSid() != nil {
		t.Errorf("started = %v", started)
	}
	rs := started.GetSpec()
	if rs.GetPadding() != 27 || rs.GetTtl() != 255 || rs.GetIpv() != gotwampv1.AddressFamily_ADDRESS_FAMILY_AUTO ||
		rs.GetPaddingFill() != gotwampv1.PaddingFill_PADDING_FILL_AUTO || rs.GetDscp() != 46 || rs.GetMode() != gotwampv1.Mode_MODE_LIGHT {
		t.Errorf("resolved spec = %v", rs)
	}
	var fromKernel int
	for i, ev := range evs[1:6] {
		p := ev.GetPacket()
		if ev.GetRunId() != runID || p == nil || p.GetSeq() != uint32(i) || p.GetReflectorSeq() != uint32(i) {
			t.Fatalf("event %d = %v", i+1, ev)
		}
		if p.GetRtt().AsDuration() <= 0 || p.GetSenderTtlAtReflector() != 255 || p.GetDscpReceived() != 46 ||
			!p.GetOwdValid() || p.GetReflectorErrest().GetError() == nil {
			t.Errorf("packet %d = %v", i, p)
		}
		if p.GetT1FromKernel() {
			fromKernel++
		}
	}
	sum := evs[6].GetSummary()
	// The daemon's socket asks for T1' (Codex M2): in kernel mode, some
	// packet carries a kernel transmit time.
	if sum.GetClock().GetTimestampingMode() == gotwampv1.TimestampingMode_TIMESTAMPING_MODE_KERNEL && fromKernel == 0 {
		t.Error("kernel timestamping, but no packet has T1 from the kernel")
	}
	if sum == nil || sum.GetRunId() != runID || sum.GetSent() != 5 || sum.GetReceived() != 5 || sum.GetLost() != 0 ||
		sum.GetRtt().GetSamples() != 5 || sum.GetRtt().GetMedian() == nil || !sum.GetLossSplitValid() ||
		sum.GetHops().GetFwd() != 0 || sum.GetClock().GetTimestampingMode() == gotwampv1.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED ||
		sum.GetKind() != gotwampv1.SummaryKind_SUMMARY_KIND_RUN || sum.GetSpec().GetCount() != 5 {
		t.Errorf("summary = %v", sum)
	}
}

// TestRunTestFull measures the daemon's own TWAMP-Control server and Full
// reflector over the loopback.
func TestRunTestFull(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	ctrl := r.d.ControlAddrs()[0]
	spec := &gotwampv1.TestSpec{
		Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(ctrl.Port()),
		Count: proto.Uint32(4), Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(time.Second),
		Dscp: proto.Uint32(46),
	}
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	if len(evs) != 6 {
		t.Fatalf("%d events, want started + 4 packets + summary: %v", len(evs), evs)
	}
	started := evs[0].GetStarted()
	if started == nil || len(started.GetSid()) != 16 || started.GetLocal().GetAddress() != "127.0.0.1" ||
		started.GetRemote().GetPort() == 0 || started.GetSpec().GetMode() != gotwampv1.Mode_MODE_FULL {
		t.Errorf("started = %v", started)
	}
	for i, ev := range evs[1:5] {
		if p := ev.GetPacket(); p == nil || p.GetSeq() != uint32(i) || p.GetDscpReceived() != 46 {
			t.Errorf("event %d = %v", i+1, ev)
		}
	}
	sum := evs[5].GetSummary()
	if sum.GetReceived() != 4 || string(sum.GetSid()) != string(started.GetSid()) || sum.GetRemote().GetPort() != started.GetRemote().GetPort() {
		t.Errorf("summary = %v", sum)
	}
	// The session was released after Stop-Sessions and its Timeout.
	client := gotwampv1.NewReflectorServiceClient(r.conn)
	waitFor(t, "the Full session to be released", func() bool {
		l, err := client.ListReflectorSessions(t.Context(), &gotwampv1.ListReflectorSessionsRequest{Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL})
		return err == nil && len(l.GetFullSessions()) == 0
	})
}

func TestRunTestFullRejected(t *testing.T) {
	c := testConfig(t)
	c.Reflector.Control.MaxPadding = 10
	r := start(t, c, Options{})
	defer r.stop(t)
	spec := &gotwampv1.TestSpec{Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(r.d.ControlAddrs()[0].Port()), Count: proto.Uint32(1)}
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	if len(evs) != 1 || evs[0].GetError().GetStage() != gotwampv1.RunStage_RUN_STAGE_REQUEST || evs[0].GetError().GetAccept() != 3 {
		t.Errorf("events = %v; want a request error with Accept 3 (padding over max_padding)", evs)
	}
	// Nothing listens on the discard port: the connect stage fails.
	spec.ControlPort = 9
	stream, err = gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	if evs := collect(t, stream); len(evs) != 1 || evs[0].GetError().GetStage() != gotwampv1.RunStage_RUN_STAGE_CONNECT {
		t.Errorf("events = %v; want a connect error", evs)
	}
}

func TestStopTest(t *testing.T) {
	for _, mode := range []gotwampv1.Mode{gotwampv1.Mode_MODE_LIGHT, gotwampv1.Mode_MODE_FULL} {
		t.Run(mode.String(), func(t *testing.T) {
			c := testConfig(t)
			r := start(t, c, Options{})
			defer r.stop(t)
			client := gotwampv1.NewSenderServiceClient(r.conn)
			spec := lightSpec(r.d.LightAddrs()[0], 100_000)
			if mode == gotwampv1.Mode_MODE_FULL {
				spec.Mode, spec.ReceiverPort, spec.ControlPort = mode, nil, uint32(r.d.ControlAddrs()[0].Port())
			}
			stream, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			first, err := stream.Recv()
			if err != nil || first.GetStarted() == nil {
				t.Fatalf("first event = %v, %v", first, err)
			}
			if _, err := stream.Recv(); err != nil { // one packet
				t.Fatal(err)
			}
			if _, err := client.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: first.GetRunId()}); err != nil {
				t.Fatalf("StopTest: %v", err)
			}
			evs := collect(t, stream)
			last := evs[len(evs)-1].GetSummary()
			if last == nil || !last.GetAborted() || last.GetSent() == 0 {
				t.Errorf("last event = %v; want the aborted summary", evs[len(evs)-1])
			}
			for _, ev := range evs {
				if ev.GetError() != nil {
					t.Errorf("unexpected error event %v", ev)
				}
			}
			if _, err := client.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: first.GetRunId()}); status.Code(err) != codes.NotFound {
				t.Errorf("StopTest after the end = %v, want NotFound", err)
			}
		})
	}
}

func TestRunTestLost(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	// Nothing listens on the discard port of the loopback.
	spec := lightSpec(netip.MustParseAddrPort("127.0.0.1:9"), 2)
	spec.Timeout = durationpb.New(50 * time.Millisecond)
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	var lost int
	for _, ev := range evs {
		if ev.GetLost() != nil {
			lost++
		}
	}
	if sum := evs[len(evs)-1].GetSummary(); lost != 2 || sum.GetLost() != 2 || sum.GetRtt().GetMedian() != nil {
		t.Errorf("lost events %d, summary %v", lost, sum)
	}
}

func TestRunTestCanceledByShutdown(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	target := r.d.LightAddrs()[0]
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: lightSpec(target, 100_000)})
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetStarted() == nil {
		t.Fatalf("first event = %v, %v", ev, err)
	}
	begin := time.Now()
	r.stop(t)
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("shutdown during a run took %v", d)
	}
	var sawAbort, sawError bool
	for {
		ev, err := stream.Recv()
		if err != nil {
			break
		}
		if s := ev.GetSummary(); s != nil && s.GetAborted() {
			sawAbort = true
		}
		if e := ev.GetError(); e != nil && strings.Contains(e.GetMessage(), "shutting down") {
			sawError = true
		}
	}
	if !sawAbort || !sawError {
		t.Errorf("aborted summary %v, shutdown error %v", sawAbort, sawError)
	}
}

func TestRunTestClientCancel(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(ctx, &gotwampv1.RunTestRequest{Spec: lightSpec(r.d.LightAddrs()[0], 100_000)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(r.logs.String(), "ad-hoc test ended") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(r.logs.String(), "aborted=true") {
		t.Errorf("the run did not stop when the client canceled:\n%s", r.logs.String())
	}
}

func TestRunTestRejects(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	target := netip.MustParseAddrPort("127.0.0.1:862")
	mod := func(f func(*gotwampv1.TestSpec)) *gotwampv1.TestSpec {
		s := lightSpec(target, 3)
		f(s)
		return s
	}
	cases := []struct {
		name string
		spec *gotwampv1.TestSpec
		code codes.Code
	}{
		{"no spec", nil, codes.InvalidArgument},
		{"count 0", mod(func(s *gotwampv1.TestSpec) { s.Count = proto.Uint32(0) }), codes.InvalidArgument},
		{"count too large", mod(func(s *gotwampv1.TestSpec) { s.Count = proto.Uint32(100_001) }), codes.InvalidArgument},
		{"interval", mod(func(s *gotwampv1.TestSpec) { s.Interval = durationpb.New(time.Microsecond) }), codes.InvalidArgument},
		{"timeout", mod(func(s *gotwampv1.TestSpec) { s.Timeout = durationpb.New(0) }), codes.InvalidArgument},
		{"padding", mod(func(s *gotwampv1.TestSpec) { s.Padding = proto.Uint32(9001) }), codes.InvalidArgument},
		{"dscp", mod(func(s *gotwampv1.TestSpec) { s.Dscp = proto.Uint32(64) }), codes.InvalidArgument},
		{"ttl", mod(func(s *gotwampv1.TestSpec) { s.Ttl = 256 }), codes.InvalidArgument},
		{"padding fill", mod(func(s *gotwampv1.TestSpec) { s.PaddingFill = 9 }), codes.InvalidArgument},
		{"receiver port", mod(func(s *gotwampv1.TestSpec) { s.ReceiverPort = proto.Uint32(70000) }), codes.InvalidArgument},
		{"sender port", mod(func(s *gotwampv1.TestSpec) { s.SenderPort = proto.Uint32(70000) }), codes.InvalidArgument},
		{"port range", mod(func(s *gotwampv1.TestSpec) {
			s.Compat = &gotwampv1.CompatOptions{SenderPortRange: &gotwampv1.PortRange{First: 9, Last: 1}}
		}), codes.InvalidArgument},
		// The daemon has no sender.keys_file here.
		{"security without keys", mod(func(s *gotwampv1.TestSpec) {
			s.Security, s.KeyId = gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED, "k"
		}), codes.InvalidArgument},
		{"light with security", mod(func(s *gotwampv1.TestSpec) {
			s.Mode, s.Security = gotwampv1.Mode_MODE_LIGHT, gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED
		}), codes.InvalidArgument},
		{"key id", mod(func(s *gotwampv1.TestSpec) { s.KeyId = strings.Repeat("k", 81) }), codes.InvalidArgument},
		{"no target", mod(func(s *gotwampv1.TestSpec) { s.Target = "" }), codes.InvalidArgument},
		{"bad source", mod(func(s *gotwampv1.TestSpec) { s.SourceAddress = "nope" }), codes.InvalidArgument},
		{"family mismatch", mod(func(s *gotwampv1.TestSpec) { s.SourceAddress = "::1" }), codes.InvalidArgument},
		{"ipv6 only for an ipv4 literal", mod(func(s *gotwampv1.TestSpec) { s.Ipv = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6 }), codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: tc.spec})
			if err == nil {
				_, err = stream.Recv()
			}
			if status.Code(err) != tc.code {
				t.Errorf("RunTest = %v, want %v", err, tc.code)
			}
		})
	}
}

func TestRunTestSenderDisabled(t *testing.T) {
	c := testConfig(t)
	c.Sender.Enabled = false
	r := start(t, c, Options{})
	defer r.stop(t)
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: lightSpec(netip.MustParseAddrPort("127.0.0.1:862"), 1)})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("RunTest = %v, want FailedPrecondition", err)
	}
}

func TestPlanDefaults(t *testing.T) {
	c := testConfig(t)
	c.Sender.Defaults.Mode = "light"
	c.Sender.Defaults.Padding = 100
	c.Sender.Defaults.DSCP = 34
	c.Sender.Defaults.PaddingFill = "random"
	c.Sender.Defaults.SenderPort = 50000
	c.Sender.Defaults.TolerateZeroMultiplier = false
	c.Daemon.Clock.UserspaceError = 70 * time.Microsecond
	c.Daemon.Clock.KernelError = 3 * time.Microsecond
	c.Sender.Defaults.ReflectorSequence = "copy_sender"
	c.Sender.Defaults.DSCPECNMonitoring = true

	p, err := newPlan("reflector.example", "", "", c.Sender.Defaults, c.Daemon, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := sender.Config{
		Count:              100,
		Interval:           100 * time.Millisecond,
		Timeout:            2 * time.Second,
		Padding:            100,
		PaddingFill:        sender.PaddingRandom,
		DSCP:               34,
		TTL:                255,
		UserspaceError:     70 * time.Microsecond,
		KernelError:        3 * time.Microsecond,
		HardwareError:      time.Microsecond,
		StatelessReflector: true,
		DSCPECNMonitoring:  true,
		DropZeroMultiplier: true,
		MaxSamples:         100_000,
		TxTimestampWait:    time.Millisecond,
	}
	if p.test != want || p.full || p.receiverPort != 862 || p.senderPort != 50000 || p.senderPorts != (config.PortRange{First: 49152, Last: 57343}) {
		t.Errorf("plan = %+v\nwant test %+v", p, want)
	}

	// tx_timestamp_wait 0 does not wait, which the sender spells negative.
	c.Sender.Defaults.TxTimestampWait = 0
	if p, err := newPlan("x", "", "", c.Sender.Defaults, c.Daemon, nil); err != nil || p.test.TxTimestampWait >= 0 {
		t.Errorf("tx_timestamp_wait 0: %v, %v", p.test.TxTimestampWait, err)
	}

	// Explicit zeros in a request override non-zero defaults.
	params := c.Sender.Defaults
	if err := overlayTest(&params, &gotwampv1.TestSpec{Padding: proto.Uint32(0), Dscp: proto.Uint32(0), SenderPort: proto.Uint32(0), Mode: gotwampv1.Mode_MODE_FULL}); err != nil {
		t.Fatal(err)
	}
	if params.Padding != 0 || params.DSCP != 0 || params.SenderPort != 0 || params.Mode != "full" {
		t.Errorf("overlay = %+v", params)
	}

	r := &runner{resolve: func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if host != "reflector.example" {
			return nil, errors.New("no such host")
		}
		all := []netip.Addr{netip.MustParseAddr("2001:db8::10"), netip.MustParseAddr("192.0.2.10")}
		if network == "ip4" {
			return all[1:], nil
		}
		return all, nil
	}}
	for _, tc := range []struct {
		ipv    string
		source netip.Addr
		want   string
	}{
		{"", netip.Addr{}, "2001:db8::10"},
		{"4", netip.Addr{}, "192.0.2.10"},
		{"", netip.MustParseAddr("192.0.2.1"), "192.0.2.10"},
	} {
		if got, err := r.target(t.Context(), "reflector.example", tc.ipv, tc.source); err != nil || got.String() != tc.want {
			t.Errorf("target(ipv %q, source %v) = %v, %v; want %s", tc.ipv, tc.source, got, err, tc.want)
		}
	}
	if _, err := r.target(t.Context(), "other.example", "", netip.Addr{}); err == nil {
		t.Error("unresolvable target accepted")
	}
}

// TestStopTestWithStalledClient: a client that stops reading cannot keep
// StopTest from ending the run: the run never waits for the stream.
func TestStopTestWithStalledClient(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	spec := lightSpec(r.d.LightAddrs()[0], 100_000)
	spec.Interval = durationpb.New(time.Millisecond)
	stream, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	// Read nothing more for a while: the stream's flow control fills up.
	time.Sleep(1500 * time.Millisecond)
	if _, err := client.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: first.GetRunId()}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the run to end without the client reading", func() bool {
		return strings.Contains(r.logs.String(), "ad-hoc test ended")
	})
	// Reading again delivers the aborted summary. (Whether per-packet
	// events were dropped depends on the stream's flow-control window;
	// TestEventQueue covers the dropping.)
	var aborted bool
	for {
		ev, err := stream.Recv()
		if err != nil {
			break
		}
		aborted = aborted || ev.GetSummary().GetAborted()
	}
	if !aborted {
		t.Error("no aborted summary")
	}
}

func TestRunSummaryInvalid(t *testing.T) {
	var s sender.Summary
	s.Invalid, s.Aborted = true, true
	if p := runSummary(s, "", "r", nil, nil, gotwampv1.SummaryKind_SUMMARY_KIND_RUN); !p.GetInvalid() || !p.GetAborted() {
		t.Errorf("runSummary dropped Invalid: %v", p)
	}
}

// TestRunSummaryCounters: the counters and flags of sender.Summary reach
// the RunSummary (B21).
func TestRunSummaryCounters(t *testing.T) {
	var s sender.Summary
	s.SID = wire.SID{1}
	s.NegativeOWD, s.InvalidRTT, s.TxMissing, s.TxInvalid, s.Unsupported = 1, 2, 3, 4, 5
	s.MaxClockErrorUnknown, s.TxTimestamps = true, true
	s.Jitter.IPDVForward = stats.Distribution{Count: 2, Max: 3 * time.Millisecond, P95: 2 * time.Millisecond}
	s.Jitter.IPDVReverse = stats.Distribution{Count: 2, Max: 5 * time.Millisecond, P95: 4 * time.Millisecond}
	p := runSummary(s, "", "r", nil, nil, gotwampv1.SummaryKind_SUMMARY_KIND_RUN)
	j := p.GetJitter()
	if len(p.GetSid()) != 16 || p.GetSid()[0] != 1 || p.GetNegativeOwd() != 1 || p.GetInvalidRtt() != 2 ||
		p.GetTxTimestampMissing() != 3 || p.GetTxTimestampInvalid() != 4 || p.GetUnsupportedTimestampFormat() != 5 ||
		!p.GetClock().GetMaxClockErrorUnknown() || !p.GetClock().GetTxTimestamps() || p.GetDscpReflected() != nil ||
		j.GetIpdvFwdMax().AsDuration() != 3*time.Millisecond || j.GetIpdvFwdP95().AsDuration() != 2*time.Millisecond ||
		j.GetIpdvRevMax().AsDuration() != 5*time.Millisecond || j.GetIpdvRevP95().AsDuration() != 4*time.Millisecond {
		t.Errorf("runSummary = %v", p)
	}
	if ev := runError("internal", "panic", 0); ev.GetError().GetStage() != gotwampv1.RunStage_RUN_STAGE_INTERNAL {
		t.Errorf("stage internal = %v", ev.GetError().GetStage())
	}
}

// TestRunTestDSCPECN: with RFC 7750 on at both ends, the packets and the
// summary report the DSCP the reflector received.
func TestRunTestDSCPECN(t *testing.T) {
	c := testConfig(t)
	c.Reflector.Light.DSCPECNMonitoring = true
	r := start(t, c, Options{})
	defer r.stop(t)
	spec := lightSpec(r.d.LightAddrs()[0], 3)
	spec.Dscp = proto.Uint32(46)
	spec.DscpEcnMonitoring = proto.Bool(true)
	evs := collect(t, mustRunTest(t, gotwampv1.NewSenderServiceClient(r.conn), spec))
	var packets int
	var sum *gotwampv1.RunSummary
	for _, ev := range evs {
		if p := ev.GetPacket(); p != nil {
			packets++
			if p.DscpReflected == nil || p.GetDscpReflected() != 46 || p.EcnReflected == nil || p.GetClockOffsetHint() == nil {
				t.Errorf("packet = %v", p)
			}
		}
		if s := ev.GetSummary(); s != nil {
			sum = s
		}
	}
	d := sum.GetDscpReflected()
	if packets != 3 || d.GetCount() != 3 || d.GetMode() != 46 || d.GetRemarked() != 0 || !sum.GetSpec().GetDscpEcnMonitoring() {
		t.Errorf("%d packets, summary %v", packets, sum)
	}
}

// TestSchedErrorShuttingDown: a daemon shutting down answers Aborted, which
// the CLI does not take for an unreachable daemon (B5).
func TestSchedErrorShuttingDown(t *testing.T) {
	if c := status.Code(schedError("x", schedule.ErrClosed)); c != codes.Aborted {
		t.Errorf("code = %v, want Aborted", c)
	}
}

// TestLightSocketFailureStage: a Light run whose socket cannot be opened
// fails in the test stage, a local failure, not a protocol error (B6).
func TestLightSocketFailureStage(t *testing.T) {
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	spec := lightSpec(r.d.LightAddrs()[0], 1)
	spec.SenderPort = proto.Uint32(uint32(busy.LocalAddr().(*net.UDPAddr).Port))
	evs := collect(t, mustRunTest(t, gotwampv1.NewSenderServiceClient(r.conn), spec))
	if len(evs) != 1 || evs[0].GetError().GetStage() != gotwampv1.RunStage_RUN_STAGE_TEST {
		t.Errorf("events = %v, want one error in the test stage", evs)
	}
}

// TestRecovered: a panic in an ad-hoc run becomes an internal-stage error
// (B30).
func TestRecovered(t *testing.T) {
	_, err := recovered(func() (sender.Summary, error) { panic("boom") })
	se, ok := errors.AsType[*schedule.StageError](err)
	if !ok || se.Stage != "internal" || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if _, err := recovered(func() (sender.Summary, error) { return sender.Summary{}, nil }); err != nil {
		t.Errorf("err = %v", err)
	}
}

// TestMergeWatchers: a watch of several sessions takes the events of those
// alone, from one subscription each (B31).
func TestMergeWatchers(t *testing.T) {
	m := store.NewMemory(10, 10)
	a := m.Watch(store.WatchOptions{Session: "a", Buffer: 1})
	b := m.Watch(store.WatchOptions{Session: "b", Buffer: 1})
	defer a.Close()
	defer b.Close()
	events := mergeWatchers(t.Context(), []*store.Watcher{a, b})
	for _, name := range []string{"c", "c", "a", "b"} {
		m.Publish(store.Event{Kind: store.EventRunSkipped, Time: time.Now(), Session: &store.SessionInfo{Name: name}})
	}
	got := map[string]bool{}
	for range 2 {
		got[(<-events).Session.Name] = true
	}
	if !got["a"] || !got["b"] || a.Dropped()+b.Dropped() != 0 {
		t.Errorf("got %v, dropped %d/%d", got, a.Dropped(), b.Dropped())
	}
}
