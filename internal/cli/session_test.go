package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func TestSessionList(t *testing.T) {
	h := newHarness(t)
	code, out, errs := h.run(t.Context(), "session", "list")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, want := range []string{
		"NAME   TARGET      MODE  SCHEDULE    STATE    SOURCE  LAST RESULT",
		"tokyo  192.0.2.10  full  every 1m0s  running  api     10/10 received, loss 0.000%, rtt median 2.00 ms",
		"osaka  192.0.2.10  full  every 1m0s  running  api     error in request: accept-session accept=5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code, out, _ := h.run(t.Context(), "session", "list", "--json"); code != ExitOK || !strings.Contains(compact(t, out), `"name":"osaka"`) {
		t.Errorf("--json: %d\n%s", code, out)
	}
}

func TestSessionShow(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run(t.Context(), "session", "show", "tokyo")
	for _, want := range []string{
		"name       tokyo", "target     192.0.2.10 (full)", "schedule   every 1m0s", "state      running (api)",
		"runs       3 ok, 0 failed, 0 skipped", "labels     site=tokyo", "--- 192.0.2.10 TWAMP statistics ---",
	} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if code, _, _ := h.run(t.Context(), "session", "get", "nope"); code != ExitFailure {
		t.Errorf("show missing: exit %d", code)
	}
}

func TestSessionAddWithFlags(t *testing.T) {
	h := newHarness(t)
	code, _, errs := h.run(t.Context(), "session", "add", "lab", "--target", "192.0.2.20", "--light", "-c", "50", "-D", "ef",
		"--frequency", "30s", "--jitter", "2s", "--label", "site=lab", "--label", "link=backup")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	req := h.fake.last("CreateSession").(*gotwampv1.CreateSessionRequest).GetSpec()
	s := req.GetSpec()
	if req.GetName() != "lab" || s.GetTarget() != "192.0.2.20" || s.GetMode() != gotwampv1.Mode_MODE_LIGHT || s.GetCount() != 50 ||
		s.GetDscp() != 46 || s.Padding != nil || s.Interval != nil ||
		req.GetSchedule().GetFrequency().AsDuration() != 30*time.Second || req.GetSchedule().GetJitter().AsDuration() != 2*time.Second ||
		req.GetLabels()["link"] != "backup" {
		t.Errorf("CreateSession = %v", req)
	}

	code, _, _ = h.run(t.Context(), "session", "update", "lab", "--target", "192.0.2.20", "--continuous", "--window", "10s")
	if req := h.fake.last("UpdateSession").(*gotwampv1.UpdateSessionRequest).GetSpec(); code != ExitOK ||
		req.GetSchedule().GetType() != gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS || req.GetSchedule().GetWindow().AsDuration() != 10*time.Second ||
		req.GetSpec().Count != nil {
		t.Errorf("UpdateSession: exit %d, %v", code, req)
	}
	for _, args := range [][]string{{"session", "add"}, {"session", "add", "--target", "x"}, {"session", "add", "a", "b"}} {
		if code, _, _ := h.run(t.Context(), args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

func TestSessionAddFromFile(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	body := `name: osaka
target: 2001:db8::10
ipv: "6"
mode: light
receiver_port: 862
padding: 0
dscp: af41
sender_port_range: "50000-50010"
start_time: now
tolerate_zero_multiplier: false
schedule: {type: continuous, window: 60s}
labels: {site: osaka}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := h.run(t.Context(), "session", "add", "-f", path); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	req := h.fake.last("CreateSession").(*gotwampv1.CreateSessionRequest).GetSpec()
	s, c := req.GetSpec(), req.GetSpec().GetCompat()
	if req.GetName() != "osaka" || s.GetIpv() != gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6 || s.GetMode() != gotwampv1.Mode_MODE_LIGHT ||
		s.GetReceiverPort() != 862 || s.Padding == nil || s.GetPadding() != 0 || s.GetDscp() != 34 || s.Count != nil || s.Interval != nil ||
		c.GetSenderPortRange().GetFirst() != 50000 || c.GetStartTime() != gotwampv1.StartTime_START_TIME_NOW || c.TolerateZeroMultiplier == nil ||
		c.GetTolerateZeroMultiplier() || c.AcceptAlternatePort != nil || req.GetSchedule().GetType() != gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS {
		t.Errorf("CreateSession = %v", req)
	}

	// NAME may restate the file's name, not contradict it.
	if code, _, _ := h.run(t.Context(), "session", "update", "osaka", "-f", path); code != ExitOK {
		t.Errorf("update with the same name: %d", code)
	}
	if code, _, errs := h.run(t.Context(), "session", "update", "kyoto", "-f", path); code != ExitUsage || !strings.Contains(errs, "differs") {
		t.Errorf("update with another name: %d %q", code, errs)
	}
}

func TestParseSessionFile(t *testing.T) {
	bad := map[string]string{
		"unknown key":   "name: a\ntargett: x\n",
		"empty":         "",
		"mode":          "mode: half\n",
		"ipv":           "ipv: 5\n",
		"fill":          "padding_fill: ones\n",
		"start time":    "start_time: later\n",
		"schedule":      "schedule: {type: sometimes}\n",
		"port range":    "sender_port_range: 9-1\n",
		"dscp":          "dscp: af99\n",
		"duration type": "interval: 5\n",
		"unspecified":   "mode: unspecified\n",
		"sequence":      "reflector_sequence: stateless\n",
		"security":      "security: open\n",
		"tx wait":       "tx_timestamp_wait: 2ms\n",
		"extension":     "extensions: [stamp]\n",
	}
	for name, doc := range bad {
		if _, err := parseSessionFile([]byte(doc)); err == nil {
			t.Errorf("%s: accepted %q", name, doc)
		}
	}
	s, err := parseSessionFile([]byte("name: a\ntarget: x\ncontrol_port: 8620\nttl: 64\ninterval: 1s\ncontrol_dscp: cs6\nzero_addresses: true\nend_delay: 1s\nmode: full\npadding_fill: random\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ts := s.GetSpec(); ts.GetControlPort() != 8620 || ts.GetTtl() != 64 || ts.GetInterval().AsDuration() != time.Second ||
		ts.GetCompat().GetControlDscp() != 48 || !ts.GetCompat().GetZeroAddresses() || ts.GetCompat().GetEndDelay().AsDuration() != time.Second ||
		ts.GetMode() != gotwampv1.Mode_MODE_FULL || ts.GetPaddingFill() != gotwampv1.PaddingFill_PADDING_FILL_RANDOM ||
		s.GetSchedule().GetType() != gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC {
		t.Errorf("parsed = %v", s)
	}
	// Every test key of sender.sessions[] that the API takes is read, so
	// that a session copied from gotwampd.yaml is accepted.
	s, err = parseSessionFile([]byte("name: a\ntarget: x\nmode: light\nreflector_sequence: copy_sender\ndscp_ecn_monitoring: true\n" +
		"security: authenticated\nkey_id: alice\nextensions: [reflect_octets]\noctets_to_reflect: 4\nstamp: true\nssid: 9\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ts := s.GetSpec(); ts.GetCompat().GetReflectorSequence() != gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_COPY_SENDER ||
		!ts.GetDscpEcnMonitoring() || ts.GetSecurity() != gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED || ts.GetKeyId() != "alice" ||
		len(ts.GetExtensions()) != 1 || ts.GetExtensions()[0] != gotwampv1.Extension_EXTENSION_REFLECT_OCTETS || ts.GetOctetsToReflect() != 4 ||
		!ts.GetStamp() || ts.GetSsid() != 9 {
		t.Errorf("parsed = %v", s)
	}
}

func TestSessionStateAndRemove(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ verb, want string }{{"stop", "session tokyo stopped"}, {"start", "session tokyo running"}, {"remove", "session tokyo removed"}} {
		if code, out, _ := h.run(t.Context(), "session", tc.verb, "tokyo"); code != ExitOK || !strings.Contains(out, tc.want) {
			t.Errorf("%s: %d %q", tc.verb, code, out)
		}
	}
	if code, out, _ := h.run(t.Context(), "session", "stop", "tokyo", "--json"); code != ExitOK || !strings.Contains(out, "SESSION_STATE_STOPPED") {
		t.Errorf("stop --json: %d %q", code, out)
	}
	if code, out, _ := h.run(t.Context(), "session", "rm", "tokyo", "--json"); code != ExitOK || strings.TrimSpace(out) != "{}" {
		t.Errorf("rm --json: %d %q", code, out)
	}
}

func TestStatsAndWatch(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run(t.Context(), "stats", "tokyo", "--last", "2", "--since", "1h")
	if code != ExitOK || !strings.Contains(out, "2026-10-04 12:00:00  100   5     5.000%  1.00/2.00/3.00") {
		t.Errorf("stats: %d\n%s", code, out)
	}
	req := h.fake.last("GetSessionStats").(*gotwampv1.GetSessionStatsRequest)
	if req.GetLast() != 2 || time.Since(req.GetSince().AsTime()) < 59*time.Minute {
		t.Errorf("GetSessionStats request = %v", req)
	}
	// --since alone asks for the whole range; without it, the latest 10.
	if _, _, _ = h.run(t.Context(), "stats", "tokyo", "--since", "24h"); h.fake.last("GetSessionStats").(*gotwampv1.GetSessionStatsRequest).GetLast() != 0 {
		t.Errorf("--since alone: %v", h.fake.last("GetSessionStats"))
	}
	if _, _, _ = h.run(t.Context(), "stats", "tokyo"); h.fake.last("GetSessionStats").(*gotwampv1.GetSessionStatsRequest).GetLast() != 10 {
		t.Errorf("no --since: %v", h.fake.last("GetSessionStats"))
	}

	h.fake.watch = []*gotwampv1.TestEvent{
		{Session: "tokyo", Event: &gotwampv1.TestEvent_Summary{Summary: &gotwampv1.RunSummary{Sent: 3, Received: 3, Kind: gotwampv1.SummaryKind_SUMMARY_KIND_WINDOW}}},
		{Session: "tokyo", Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{Stage: gotwampv1.RunStage_RUN_STAGE_CONNECT, Message: "refused"}}},
		{Session: "tokyo", Event: &gotwampv1.TestEvent_Lost{Lost: &gotwampv1.PacketLost{Seq: 4}}},
		{Event: &gotwampv1.TestEvent_Dropped{Dropped: &gotwampv1.EventsDropped{Count: 7}}},
	}
	code, out, _ = h.run(t.Context(), "watch", "tokyo", "--packets")
	for _, want := range []string{"tokyo            window 3/3 received", "error in connect: refused", "seq=4   *LOST*", "(7 events dropped"} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("watch lacks %q:\n%s", want, out)
		}
	}
	if req := h.fake.last("WatchResults").(*gotwampv1.WatchResultsRequest); !req.GetPackets() || req.GetSessions()[0] != "tokyo" {
		t.Errorf("WatchResults request = %v", req)
	}
	code, out, _ = h.run(t.Context(), "watch", "--json")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); code != ExitOK || len(lines) != 4 || !json.Valid([]byte(lines[0])) {
		t.Errorf("watch --json: %d\n%s", code, out)
	}
}

func TestReflectorAndReload(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run(t.Context(), "reflector", "status")
	for _, want := range []string{
		"light  0.0.0.0:862  7         6", "full   0.0.0.0:862  -         -", "control connections: 1 open, 4 accepted, 0 rejected",
		"light flows: 2", "light  7         6          acl 1",
	} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("reflector status lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = h.run(t.Context(), "reflector", "sessions")
	for _, want := range []string{"c0000205010203040506070809", "192.0.2.1:40000", "encrypted", "active", "[2001:db8::1]:64374"} {
		if code != ExitOK || !strings.Contains(out, want) {
			t.Errorf("reflector sessions lacks %q:\n%s", want, out)
		}
	}
	if code, out, _ := h.run(t.Context(), "reflector", "status", "--json"); code != ExitOK || !strings.Contains(compact(t, out), `"enabled":true`) {
		t.Errorf("reflector status --json: %d", code)
	}
	code, out, _ = h.run(t.Context(), "config", "reload")
	if code != ExitOK || !strings.Contains(out, "sessions added:   new") || !strings.Contains(out, "sessions unchanged: 2") ||
		!strings.Contains(out, "warning: restart gotwampd") {
		t.Errorf("config reload: %d\n%s", code, out)
	}
}

// compact removes the insignificant whitespace of JSON, which protojson
// varies on purpose.
func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return b.String()
}
