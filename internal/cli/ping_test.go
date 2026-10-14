package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func dur(d time.Duration) *durationpb.Duration { return durationpb.New(d) }

func delays(lo, median, mean, hi time.Duration) *gotwampv1.DelayStats {
	return &gotwampv1.DelayStats{Min: dur(lo), Median: dur(median), Mean: dur(mean), Max: dur(hi), P95: dur(hi), P99: dur(hi), Stddev: dur(100 * time.Microsecond), Samples: 2}
}

// scriptedRun is a Light run of 3 packets: one reply, one forward duplicate,
// one reordered reply, one lost packet.
func scriptedRun() []*gotwampv1.TestEvent {
	ev := func(e any) *gotwampv1.TestEvent {
		t := &gotwampv1.TestEvent{RunId: "01RUN"}
		switch v := e.(type) {
		case *gotwampv1.RunStarted:
			t.Event = &gotwampv1.TestEvent_Started{Started: v}
		case *gotwampv1.PacketResult:
			t.Event = &gotwampv1.TestEvent_Packet{Packet: v}
		case *gotwampv1.PacketLost:
			t.Event = &gotwampv1.TestEvent_Lost{Lost: v}
		case *gotwampv1.RunSummary:
			t.Event = &gotwampv1.TestEvent_Summary{Summary: v}
		}
		return t
	}
	packet := func(seq uint32) *gotwampv1.PacketResult {
		return &gotwampv1.PacketResult{
			Seq: seq, Rtt: dur(8120 * time.Microsecond), FwdDelay: dur(4010 * time.Microsecond), RevDelay: dur(4100 * time.Microsecond),
			ReflectorDelay: dur(11 * time.Microsecond), OwdValid: true, SenderTtlAtReflector: 251, ReflectorTtlReceived: 251,
		}
	}
	dup := packet(0)
	dup.Duplicate, dup.DuplicateKind = true, gotwampv1.DuplicateKind_DUPLICATE_KIND_FORWARD
	reordered := packet(2)
	reordered.Reordered, reordered.OwdValid = true, false
	return []*gotwampv1.TestEvent{
		ev(&gotwampv1.RunStarted{
			Spec:  &gotwampv1.TestSpec{Target: "192.0.2.10", Mode: gotwampv1.Mode_MODE_LIGHT, Count: proto.Uint32(3), Interval: dur(100 * time.Millisecond), Padding: proto.Uint32(27), Dscp: proto.Uint32(46)},
			Local: &gotwampv1.Endpoint{Address: "192.0.2.1", Port: 50000}, Remote: &gotwampv1.Endpoint{Address: "192.0.2.10", Port: 862},
			StartedAt: timestamppb.Now(),
		}),
		ev(packet(0)),
		ev(dup),
		ev(reordered),
		ev(&gotwampv1.PacketLost{Seq: 1}),
		ev(&gotwampv1.RunSummary{
			Sent: 3, Received: 2, Lost: 1, LossRatio: 1.0 / 3, DupForward: 1, Reordered: 1,
			Rtt:            delays(8120*time.Microsecond, 8200*time.Microsecond, 8210*time.Microsecond, 8300*time.Microsecond),
			FwdDelay:       delays(4010*time.Microsecond, 4050*time.Microsecond, 4050*time.Microsecond, 4520*time.Microsecond),
			RevDelay:       delays(4100*time.Microsecond, 4220*time.Microsecond, 4220*time.Microsecond, 4610*time.Microsecond),
			ReflectorDelay: delays(9*time.Microsecond, 10*time.Microsecond, 10*time.Microsecond, 15*time.Microsecond),
			OwdValid:       true,
			Jitter:         &gotwampv1.JitterStats{IpdvRttMean: dur(40 * time.Microsecond), IpdvRttMax: dur(310 * time.Microsecond), PdvRttP95: dur(490 * time.Microsecond), JitterRfc3550: dur(50 * time.Microsecond)},
			Hops:           &gotwampv1.HopStats{Fwd: 4, Rev: 4},
			Clock:          &gotwampv1.ClockInfo{ReflectorSynced: true, MaxClockError: dur(210 * time.Microsecond), TimestampingMode: gotwampv1.TimestampingMode_TIMESTAMPING_MODE_USERSPACE},
		}),
	}
}

func TestPingTable(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()
	code, out, errs := h.run(t.Context(), "ping", "--light", "192.0.2.10", "-c", "3")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	want := `TWAMP light 192.0.2.1:50000 -> reflector 192.0.2.10:862, 3 packets, interval 100ms, padding 27, dscp ef
seq=0   rtt=8.12ms  fwd=4.01ms  rev=4.10ms  refl=11us  ttl=251/251
seq=0   rtt=8.12ms  fwd=4.01ms  rev=4.10ms  refl=11us  ttl=251/251  DUP (forward)
seq=2   rtt=8.12ms  fwd=4.01ms  rev=4.10ms  refl=11us  ttl=251/251 (unsync)  reordered
seq=1   *LOST*
--- 192.0.2.10 TWAMP statistics ---
3 sent, 1 lost (33.333%), 1 dup (fwd 1 / rev 0), 1 reordered
rtt      min/median/mean/max = 8.12/8.20/8.21/8.30 ms, p95 8.30 ms, stddev 0.10 ms
forward  min/median/max = 4.01/4.05/4.52 ms   (clocks synchronized, max error 0.21 ms)
reverse  min/median/max = 4.10/4.22/4.61 ms
reflector processing min/max = 9/15 us
jitter   ipdv mean 0.04 ms, max 0.31 ms; pdv p95 0.49 ms; rfc3550 0.05 ms
hops     forward 4, reverse 4
timestamps: userspace (sender), reflector synced: yes
`
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	// Only the flags given are sent; the rest inherit gotwampd's defaults.
	spec := h.fake.lastSpec()
	if spec.GetMode() != gotwampv1.Mode_MODE_LIGHT || spec.GetCount() != 3 || spec.Padding != nil || spec.Dscp != nil ||
		spec.Interval != nil || spec.ReceiverPort != nil || spec.Compat != nil {
		t.Errorf("spec = %v", spec)
	}
}

func TestPingQuietAndJSON(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()
	_, out, _ := h.run(t.Context(), "ping", "--light", "-q", "192.0.2.10")
	if strings.Contains(out, "seq=") || !strings.Contains(out, "--- 192.0.2.10 TWAMP statistics ---") {
		t.Errorf("quiet output:\n%s", out)
	}

	code, out, _ := h.run(t.Context(), "ping", "--light", "--output", "jsonl", "192.0.2.10")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != ExitOK || len(lines) != 6 || !strings.Contains(lines[5], `"summary"`) || !strings.Contains(lines[0], `"run_id":"01RUN"`) {
		t.Errorf("jsonl: %d\n%s", code, out)
	}

	code, out, _ = h.run(t.Context(), "ping", "--light", "--json", "-q", "192.0.2.10")
	var doc struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &doc); code != ExitOK || err != nil || len(doc.Events) != 2 {
		t.Errorf("json -q: %d %v\n%s", code, err, out)
	}
}

func TestPingFlags(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()
	code, _, errs := h.run(t.Context(), "ping", "--light", "-6", "-c", "10", "-i", "0.05", "-L", "500ms", "-s", "0",
		"--padding-fill", "zero", "-D", "af41", "--ttl", "64", "-r", "8620", "--sender-port", "50001", "-S", "::1",
		"-Z", "--start-time", "now", "--control-timeout", "3", "--reflector-sequence", "copy_sender", "--dscp-ecn", "reflector.example")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	s := h.fake.lastSpec()
	ok := s.GetTarget() == "reflector.example" && s.GetIpv() == gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6 &&
		s.GetCount() == 10 && s.GetInterval().AsDuration() == 50*time.Millisecond && s.GetTimeout().AsDuration() == 500*time.Millisecond &&
		s.Padding != nil && s.GetPadding() == 0 && s.GetPaddingFill() == gotwampv1.PaddingFill_PADDING_FILL_ZERO &&
		s.GetDscp() == 34 && s.GetTtl() == 64 && s.GetReceiverPort() == 8620 && s.GetSenderPort() == 50001 &&
		s.GetSourceAddress() == "::1" && s.GetCompat().GetZeroAddresses() && s.GetCompat().GetStartTime() == gotwampv1.StartTime_START_TIME_NOW &&
		s.GetControlTimeout().AsDuration() == 3*time.Second && s.GetDscpEcnMonitoring() &&
		s.GetCompat().GetReflectorSequence() == gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_COPY_SENDER
	if !ok {
		t.Errorf("spec = %v", s)
	}
	// --reflect-octets is the two-octet echo value, in hex too; the length
	// of padding to reflect is --padding-to-reflect.
	if _, out, _ := h.run(t.Context(), "ping", "--help"); !strings.Contains(out, "server must echo in Accept-Session") ||
		!strings.Contains(out, "padding the reflector must return unchanged") {
		t.Errorf("ping --help on Reflect Octets:\n%s", out)
	}
	if _, _, _ = h.run(t.Context(), "ping", "--reflect-octets", "0xbeef", "x"); h.fake.lastSpec().GetOctetsToReflect() != 0xbeef {
		t.Errorf("--reflect-octets 0xbeef: %v", h.fake.lastSpec())
	}
	if _, _, _ = h.run(t.Context(), "ping", "--isc", "--symmetrical", "--reflect-octets", "8", "--padding-to-reflect", "20", "x"); !slices.Equal(h.fake.lastSpec().GetExtensions(),
		[]gotwampv1.Extension{gotwampv1.Extension_EXTENSION_INDIVIDUAL_SESSION_CONTROL, gotwampv1.Extension_EXTENSION_SYMMETRICAL_SIZE}) ||
		h.fake.lastSpec().GetOctetsToReflect() != 8 || h.fake.lastSpec().GetPaddingToReflect() != 20 {
		t.Errorf("Full extensions: %v", h.fake.lastSpec())
	}
	if _, _, _ = h.run(t.Context(), "ping", "--light", "--stamp", "--ssid", "7", "x"); !h.fake.lastSpec().GetStamp() || h.fake.lastSpec().GetSsid() != 7 {
		t.Errorf("STAMP: %v", h.fake.lastSpec())
	}
	if _, _, _ = h.run(t.Context(), "ping", "--security", "auth", "--key-id", "alice", "x"); h.fake.lastSpec().GetSecurity() != gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED ||
		h.fake.lastSpec().GetKeyId() != "alice" {
		t.Errorf("--security auth --key-id alice: %v", h.fake.lastSpec())
	}
	if _, _, _ = h.run(t.Context(), "ping", "-4", "-p", "8620", "x"); h.fake.lastSpec().GetIpv() != gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV4 ||
		h.fake.lastSpec().GetMode() != gotwampv1.Mode_MODE_FULL || h.fake.lastSpec().GetControlPort() != 8620 {
		t.Errorf("-4 without --light: %v", h.fake.lastSpec())
	}

	for _, args := range [][]string{
		{"ping", "-4", "-6", "x"},
		{"ping", "-D", "af99", "x"},
		{"ping", "--padding-fill", "ones", "x"},
		{"ping", "--start-time", "later", "x"},
		{"ping", "-i", "soon", "x"},
		{"ping", "-c", "-1", "x"},
		{"ping", "--light", "-p", "18620", "x"},
		{"ping", "--ttl", "0", "x"},
		{"ping", "-p", "0", "x"},
		{"ping", "--reflector-sequence", "random", "x"},
		{"ping", "--security", "tls", "x"},
		{"ping", "--light", "--key-id", "k", "x"},
		{"ping", "--light", "--security", "encrypted", "x"},
		{"ping", "--light", "--isc", "x"},
		{"ping", "--light", "--reflect-octets", "4", "x"},
		{"ping", "--stamp", "x"},
		{"ping", "--light", "--ssid", "3", "x"},
		{"ping", "--light", "--stamp", "--ssid", "70000", "x"},
		{"ping", "--reflect-octets", "70000", "x"},
	} {
		if code, _, _ := h.run(t.Context(), args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestPingFailures(t *testing.T) {
	h := newHarness(t)
	h.fake.runErr = status.Error(codes.Unimplemented, "TWAMP Full arrives in M3")
	if code, _, errs := h.run(t.Context(), "ping", "192.0.2.10"); code != ExitProtocol || !strings.Contains(errs, "M3") {
		t.Errorf("full: %d %q", code, errs)
	}
	h.fake.runErr = status.Error(codes.InvalidArgument, "cannot resolve target")
	if code, _, _ := h.run(t.Context(), "ping", "--light", "nowhere"); code != ExitUsage {
		t.Errorf("invalid: %d", code)
	}
	h.fake.runErr = status.Error(codes.FailedPrecondition, "the sender role is disabled")
	if code, _, _ := h.run(t.Context(), "ping", "--light", "x"); code != ExitFailure {
		t.Errorf("disabled: %d", code)
	}
	h.fake.runErr = nil

	// Every packet lost: the summary is printed and the exit status is 1.
	h.fake.events = []*gotwampv1.TestEvent{
		{Event: &gotwampv1.TestEvent_Lost{Lost: &gotwampv1.PacketLost{Seq: 0}}},
		{Event: &gotwampv1.TestEvent_Summary{Summary: &gotwampv1.RunSummary{Sent: 1, Lost: 1, LossRatio: 1, Rtt: &gotwampv1.DelayStats{}}}},
	}
	code, out, errs := h.run(t.Context(), "ping", "--light", "x")
	if code != ExitFailure || !strings.Contains(out, "rtt      no replies") || errs != "" {
		t.Errorf("all lost: %d %q %q", code, out, errs)
	}

	// A failure during the run, without a summary.
	h.fake.events = []*gotwampv1.TestEvent{{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_TEST, Message: "sendmsg: no route"}}}}
	if code, out, _ := h.run(t.Context(), "ping", "--light", "x"); code != ExitFailure || !strings.Contains(out, "error in test: sendmsg: no route") {
		t.Errorf("run error: %d %q", code, out)
	}
	h.fake.events = []*gotwampv1.TestEvent{{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_REQUEST, Message: "accept-session accept=5", Accept: 5}}}}
	if code, _, _ := h.run(t.Context(), "ping", "x"); code != ExitProtocol {
		t.Errorf("control error: %d", code)
	}
	// A failure inside gotwampd is no protocol error (B6, B16).
	h.fake.events = []*gotwampv1.TestEvent{{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_INTERNAL, Message: "panic: x"}}}}
	if code, out, _ := h.run(t.Context(), "ping", "x"); code != ExitFailure || !strings.Contains(out, "error in internal") {
		t.Errorf("internal error: %d %q", code, out)
	}
	h.fake.events = nil
	if code, _, errs := h.run(t.Context(), "ping", "--light", "x"); code != ExitFailure || !strings.Contains(errs, "without a summary") {
		t.Errorf("no summary: %d %q", code, errs)
	}
}

func TestPingInterrupted(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()[:3]
	h.fake.block = true
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	code, out, _ := h.run(ctx, "ping", "--light", "192.0.2.10")
	if code != ExitInterrupted || !strings.Contains(out, "--- 192.0.2.10 TWAMP statistics (interrupted) ---\n2 replies, 0 lost so far") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestSecondsFlag(t *testing.T) {
	for in, want := range map[string]time.Duration{"0.1": 100 * time.Millisecond, "2": 2 * time.Second, "250ms": 250 * time.Millisecond, "1s": time.Second} {
		var s seconds
		if err := s.Set(in); err != nil || time.Duration(s) != want {
			t.Errorf("Set(%q) = %v, %v", in, time.Duration(s), err)
		}
	}
	var s seconds
	if s.Set("x") == nil || s.Type() != "duration" {
		t.Error("bad value accepted")
	}
}

// TestPingStopOnInterrupt: the first interrupt makes gotwampd stop the run,
// whose aborted summary is shown before gotwamp exits with 130.
func TestPingStopOnInterrupt(t *testing.T) {
	h := newHarness(t)
	run := scriptedRun()
	h.fake.events = run[:2]
	aborted := proto.Clone(run[5]).(*gotwampv1.TestEvent)
	aborted.GetSummary().Aborted = true
	h.fake.stopEvents = []*gotwampv1.TestEvent{aborted}
	interrupts := make(chan struct{}, 2)
	time.AfterFunc(200*time.Millisecond, func() { interrupts <- struct{}{} })
	code, out, _ := h.runInterrupted(t.Context(), interrupts, "ping", "--light", "192.0.2.10")
	if code != ExitInterrupted || !strings.Contains(out, "--- 192.0.2.10 TWAMP statistics (aborted) ---") {
		t.Errorf("exit %d:\n%s", code, out)
	}
	if req := h.fake.last("StopTest").(*gotwampv1.StopTestRequest); req.GetRunId() != "01RUN" {
		t.Errorf("StopTest request = %v", req)
	}
}

// TestPingSecondInterrupt: when the run does not stop, a second interrupt
// gives up on it.
func TestPingSecondInterrupt(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()[:2]
	h.fake.ignoreStop = true
	interrupts := make(chan struct{}, 2)
	time.AfterFunc(200*time.Millisecond, func() { interrupts <- struct{}{} })
	time.AfterFunc(400*time.Millisecond, func() { interrupts <- struct{}{} })
	code, out, _ := h.runInterrupted(t.Context(), interrupts, "ping", "--light", "192.0.2.10")
	if code != ExitInterrupted || !strings.Contains(out, "(interrupted) ---\n1 replies, 0 lost so far") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// TestInterruptOtherCommands: commands other than ping stop at once.
func TestInterruptOtherCommands(t *testing.T) {
	h := newHarness(t)
	h.fake.watch = nil
	interrupts := make(chan struct{}, 1)
	interrupts <- struct{}{}
	code, _, _ := h.runInterrupted(t.Context(), interrupts, "watch")
	if code != ExitInterrupted && code != ExitOK {
		t.Errorf("watch interrupted: exit %d", code)
	}
}

// started is a RunStarted whose resolved spec takes run to complete.
func started(count uint32, interval, timeout time.Duration) *gotwampv1.TestEvent {
	return &gotwampv1.TestEvent{RunId: "01RUN", Event: &gotwampv1.TestEvent_Started{Started: &gotwampv1.RunStarted{
		Spec: &gotwampv1.TestSpec{Target: "x", Mode: gotwampv1.Mode_MODE_LIGHT, Count: proto.Uint32(count), Interval: dur(interval), Timeout: dur(timeout)},
	}}}
}

// TestPingDeadlineFromResolvedSpec: the RPC deadline follows the spec
// gotwampd resolved, not the CLI's idea of the defaults (the count and the
// interval may come from sender.defaults).
func TestPingDeadlineFromResolvedSpec(t *testing.T) {
	h := newHarness(t)
	summary := scriptedRun()[5]
	// 2 × --timeout is 200ms; the run of 10 × 50ms + 100ms needs 600ms.
	h.fake.events = []*gotwampv1.TestEvent{started(10, 50*time.Millisecond, 100*time.Millisecond), summary}
	h.fake.pause = 400 * time.Millisecond
	if code, _, errs := h.run(t.Context(), "--rpc-timeout", "100ms", "ping", "--light", "-q", "x"); code != ExitOK {
		t.Errorf("run within its resolved length: exit %d: %s", code, errs)
	}
	// A run that overstays its resolved length is cut off.
	h.fake.events = []*gotwampv1.TestEvent{started(1, time.Millisecond, time.Millisecond), summary}
	h.fake.pause = time.Second
	code, _, errs := h.run(t.Context(), "--rpc-timeout", "100ms", "ping", "--light", "-q", "x")
	if code != ExitFailure || !strings.Contains(errs, "did not finish the run in time") {
		t.Errorf("overdue run: exit %d: %s", code, errs)
	}
	// Without -c the count is left to gotwampd.
	if s := h.fake.lastSpec(); s.Count != nil {
		t.Errorf("count sent without -c: %v", s.GetCount())
	}
}

func TestRunDeadline(t *testing.T) {
	s := &gotwampv1.TestSpec{
		Count: proto.Uint32(40), Interval: dur(time.Second), Timeout: dur(2 * time.Second),
		Compat: &gotwampv1.CompatOptions{EndDelay: dur(time.Second)},
	}
	if got := runDeadline(s, 30*time.Second); got != 73*time.Second {
		t.Errorf("runDeadline = %v, want 73s", got)
	}
}

// TestPingControlFailureAfterSummary: a Full run whose control connection
// failed after the summary exits 4 and marks the results invalid.
func TestPingControlFailureAfterSummary(t *testing.T) {
	h := newHarness(t)
	run := scriptedRun()
	invalid := proto.Clone(run[5]).(*gotwampv1.TestEvent)
	invalid.GetSummary().Invalid = true
	h.fake.events = []*gotwampv1.TestEvent{
		run[0], invalid,
		{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_TEST, Message: "control connection lost"}}},
	}
	code, out, _ := h.run(t.Context(), "ping", "-q", "192.0.2.10")
	if code != ExitProtocol || !strings.Contains(out, "(invalid: the control connection failed)") {
		t.Errorf("invalid summary: exit %d\n%s", code, out)
	}
	h.fake.events = []*gotwampv1.TestEvent{
		run[0], run[5],
		{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_STOP, Message: "stop-sessions: broken pipe"}}},
	}
	if code, _, _ := h.run(t.Context(), "ping", "-q", "--json", "192.0.2.10"); code != ExitProtocol {
		t.Errorf("stop failure after the summary: exit %d, want %d", code, ExitProtocol)
	}
}

// TestFragmentWarning: test packets over 1472 octets (IPv4) or 1452
// (IPv6) are warned of once the resolved padding is known (B20).
func TestFragmentWarning(t *testing.T) {
	started := func(addr string, padding uint32) *gotwampv1.RunStarted {
		return &gotwampv1.RunStarted{Spec: &gotwampv1.TestSpec{Padding: &padding}, Remote: &gotwampv1.Endpoint{Address: addr}}
	}
	for _, tc := range []struct {
		addr    string
		padding uint32
		warn    bool
	}{
		{"192.0.2.1", 1458, false},
		{"192.0.2.1", 1459, true},
		{"2001:db8::1", 1438, false},
		{"2001:db8::1", 1439, true},
		{"::ffff:192.0.2.1", 1458, false},
	} {
		if got := fragmentWarning(started(tc.addr, tc.padding)); (got != "") != tc.warn {
			t.Errorf("%s padding %d: %q", tc.addr, tc.padding, got)
		}
	}
}

// TestPingFragmentWarning: the warning reaches the standard error.
func TestPingFragmentWarning(t *testing.T) {
	h := newHarness(t)
	h.fake.events = scriptedRun()
	h.fake.events[0].GetStarted().GetSpec().Padding = proto.Uint32(2000)
	if _, _, errs := h.run(t.Context(), "ping", "x"); !strings.Contains(errs, "gotwamp: warning: test packets of 2014 octets exceed 1472") {
		t.Errorf("stderr = %q", errs)
	}
}
