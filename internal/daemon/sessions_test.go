package daemon

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
)

// sessionYAML is a periodic monitoring session against the daemon's own
// reflector: 3 packets every 2 s, the first run at once.
func sessionYAML(name, mode string, port uint16, extra string) string {
	key := "receiver_port"
	if mode == "full" {
		key = "control_port"
	}
	return fmt.Sprintf("    - {name: %s, target: 127.0.0.1, mode: %s, %s: %d, count: 3, interval: 10ms, timeout: 200ms, schedule: {frequency: 2s}%s}\n",
		name, mode, key, port, extra)
}

// sessionDaemon starts a daemon whose configuration file is body with the
// listeners of testConfig; the reflector ports are fixed so that sessions
// can name them.
func sessionDaemon(t *testing.T, body string, lightPort, controlPort uint16) (*running, string) {
	t.Helper()
	return sessionDaemonMetrics(t, "", body, lightPort, controlPort)
}

// sessionDaemonMetrics is sessionDaemon with the metrics endpoint at
// metrics (empty: off).
func sessionDaemonMetrics(t *testing.T, metrics, body string, lightPort, controlPort uint16) (*running, string) {
	t.Helper()
	c := testConfig(t)
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	head := fmt.Sprintf(`daemon:
  api: {socket: %s}
  clock: {sync: synced, max_error: 100us}
  metrics: {listen: %q}
reflector:
  light: {listeners: [{listen: "127.0.0.1:%d"}]}
  control: {listen: ["127.0.0.1:%d"]}
`, c.Daemon.API.Socket, metrics, lightPort, controlPort)
	writeFile(t, path, head+body)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return start(t, cfg, Options{ConfigPath: path}), path
}

// freePorts returns a UDP and a TCP port free on the loopback right now.
func freePorts(t *testing.T) (light, control uint16) {
	t.Helper()
	r := start(t, testConfig(t), Options{})
	light, control = r.d.LightAddrs()[0].Port(), r.d.ControlAddrs()[0].Port()
	r.stop(t)
	return light, control
}

func TestSessions(t *testing.T) {
	lp, cp := freePorts(t)
	r, _ := sessionDaemon(t, "sender:\n  enabled: true\n  sessions:\n"+sessionYAML("cfg-light", "light", lp, "")+sessionYAML("cfg-full", "full", cp, ""), lp, cp)
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	ctx := t.Context()

	waitFor(t, "both sessions to complete a run", func() bool {
		l, err := client.ListSessions(ctx, &gotwampv1.ListSessionsRequest{})
		if err != nil || len(l.GetSessions()) != 2 {
			return false
		}
		for _, s := range l.GetSessions() {
			if s.GetLastSummary() == nil {
				return false
			}
		}
		return true
	})
	l, _ := client.ListSessions(ctx, &gotwampv1.ListSessionsRequest{})
	full, light := l.GetSessions()[0], l.GetSessions()[1]
	if full.GetSpec().GetName() != "cfg-full" || full.GetSource() != gotwampv1.SessionSource_SESSION_SOURCE_CONFIG ||
		full.GetState() != gotwampv1.SessionState_SESSION_STATE_RUNNING || full.GetRunsOk() < 1 ||
		full.GetLastSummary().GetReceived() != 3 || len(full.GetLastSummary().GetSid()) != 16 || full.GetLastSummary().GetSession() != "cfg-full" {
		t.Errorf("cfg-full = %v", full)
	}
	if light.GetSpec().GetSpec().GetMode() != gotwampv1.Mode_MODE_LIGHT || light.GetLastSummary().GetSid() != nil ||
		light.GetSpec().GetSchedule().GetFrequency().AsDuration() != 2*time.Second {
		t.Errorf("cfg-light = %v", light)
	}
	st, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(ctx, &gotwampv1.GetStatusRequest{})
	if err != nil || st.GetSessionCount() != 2 {
		t.Errorf("GetStatus = %v, %v; want 2 sessions", st, err)
	}

	// Sessions of the configuration file are not the API's to change.
	if _, err := client.DeleteSession(ctx, &gotwampv1.DeleteSessionRequest{Name: "cfg-light"}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("DeleteSession(config) = %v", err)
	}

	// An API session, continuous: it keeps one run going and summarizes
	// every window.
	api := &gotwampv1.SessionSpec{
		Name: "api-light",
		Spec: &gotwampv1.TestSpec{
			Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_LIGHT, ReceiverPort: proto.Uint32(uint32(lp)),
			Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(200 * time.Millisecond),
		},
		Schedule: &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS, Window: durationpb.New(300 * time.Millisecond)},
		Labels:   map[string]string{"site": "lab"},
	}
	created, err := client.CreateSession(ctx, &gotwampv1.CreateSessionRequest{Spec: api})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.GetSession().GetSource() != gotwampv1.SessionSource_SESSION_SOURCE_API || created.GetSession().GetSpec().GetLabels()["site"] != "lab" ||
		created.GetSession().GetSpec().GetSpec().GetPadding() != 27 {
		t.Errorf("created = %v", created)
	}
	if _, err := client.CreateSession(ctx, &gotwampv1.CreateSessionRequest{Spec: api}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("CreateSession twice = %v", err)
	}
	bad := &gotwampv1.SessionSpec{Name: "Bad Name", Spec: &gotwampv1.TestSpec{Target: "x"}}
	if _, err := client.CreateSession(ctx, &gotwampv1.CreateSessionRequest{Spec: bad}); status.Code(err) != codes.InvalidArgument ||
		!strings.Contains(err.Error(), "session.name") {
		t.Errorf("CreateSession(invalid) = %v", err)
	}

	// Watch the API session: window summaries keep coming.
	watch, err := client.WatchResults(ctx, &gotwampv1.WatchResultsRequest{Sessions: []string{"api-light"}})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := watch.Recv()
	if err != nil || ev.GetSession() != "api-light" || ev.GetSummary().GetKind() != gotwampv1.SummaryKind_SUMMARY_KIND_WINDOW ||
		ev.GetSummary().GetSpec().GetTarget() != "127.0.0.1" {
		t.Errorf("watched event = %v, %v", ev, err)
	}

	stats, err := client.GetSessionStats(ctx, &gotwampv1.GetSessionStatsRequest{Name: "api-light", Last: 1})
	if err != nil || len(stats.GetSummaries()) != 1 || stats.GetSummaries()[0].GetSession() != "api-light" {
		t.Errorf("GetSessionStats = %v, %v", stats, err)
	}
	future, err := client.GetSessionStats(ctx, &gotwampv1.GetSessionStatsRequest{Name: "cfg-full", Since: timestamppb.New(time.Now().Add(time.Hour))})
	if err != nil || len(future.GetSummaries()) != 0 {
		t.Errorf("GetSessionStats(since future) = %v, %v", future, err)
	}

	// Stop, update while stopped (it stays stopped), start, delete.
	stopped, err := client.StopSession(ctx, &gotwampv1.StopSessionRequest{Name: "api-light"})
	if err != nil || stopped.GetSession().GetState() != gotwampv1.SessionState_SESSION_STATE_STOPPED {
		t.Errorf("StopSession = %v, %v", stopped, err)
	}
	api.Spec.Padding = proto.Uint32(64)
	updated, err := client.UpdateSession(ctx, &gotwampv1.UpdateSessionRequest{Spec: api})
	if err != nil || updated.GetSession().GetState() != gotwampv1.SessionState_SESSION_STATE_STOPPED ||
		updated.GetSession().GetSpec().GetSpec().GetPadding() != 64 {
		t.Errorf("UpdateSession = %v, %v", updated, err)
	}
	if s, err := client.StartSession(ctx, &gotwampv1.StartSessionRequest{Name: "api-light"}); err != nil ||
		s.GetSession().GetState() != gotwampv1.SessionState_SESSION_STATE_RUNNING {
		t.Errorf("StartSession = %v, %v", s, err)
	}
	if _, err := client.DeleteSession(ctx, &gotwampv1.DeleteSessionRequest{Name: "api-light"}); err != nil {
		t.Errorf("DeleteSession = %v", err)
	}
	for _, call := range []func() error{
		func() error {
			_, err := client.GetSession(ctx, &gotwampv1.GetSessionRequest{Name: "api-light"})
			return err
		},
		func() error {
			_, err := client.StartSession(ctx, &gotwampv1.StartSessionRequest{Name: "nope"})
			return err
		},
		func() error {
			_, err := client.UpdateSession(ctx, &gotwampv1.UpdateSessionRequest{Spec: api})
			return err
		},
		func() error {
			_, err := client.GetSessionStats(ctx, &gotwampv1.GetSessionStatsRequest{Name: "nope"})
			return err
		},
	} {
		if status.Code(call()) != codes.NotFound {
			t.Errorf("call on a missing session = %v, want NotFound", call())
		}
	}
}

func TestSessionsSenderDisabled(t *testing.T) {
	c := testConfig(t)
	c.Sender.Enabled = false
	r := start(t, c, Options{})
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	if _, err := client.ListSessions(t.Context(), &gotwampv1.ListSessionsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("ListSessions = %v", err)
	}
	w, err := client.WatchResults(t.Context(), &gotwampv1.WatchResultsRequest{})
	if err == nil {
		_, err = w.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("WatchResults = %v", err)
	}
}

// TestReloadAppliesDifferences checks PLAN M5's DoD: a reload recreates the
// changed sessions only, and a run of an unchanged one goes on.
func TestReloadAppliesDifferences(t *testing.T) {
	lp, cp := freePorts(t)
	// "long" runs for 5 s (500 packets every 10 ms).
	long := "    - {name: long, target: 127.0.0.1, mode: light, receiver_port: " + fmt.Sprint(lp) +
		", count: 500, interval: 10ms, timeout: 200ms, schedule: {frequency: 10s}}\n"
	r, path := sessionDaemon(t, "sender:\n  enabled: true\n  sessions:\n"+long+sessionYAML("change", "light", lp, "")+sessionYAML("drop", "light", lp, ""), lp, cp)
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	waitFor(t, "the long run to start", func() bool {
		s, err := client.GetSession(t.Context(), &gotwampv1.GetSessionRequest{Name: "long"})
		return err == nil && s.GetSession().GetRunning()
	})
	watch, err := client.WatchResults(t.Context(), &gotwampv1.WatchResultsRequest{Sessions: []string{"long"}})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := os.ReadFile(path)
	next := strings.Replace(string(body), sessionYAML("change", "light", lp, ""), sessionYAML("change", "light", lp, ", padding: 64"), 1)
	next = strings.Replace(next, sessionYAML("drop", "light", lp, ""), sessionYAML("new", "light", lp, ""), 1)
	next = strings.Replace(next, "metrics: {listen: \"\"}", "metrics: {listen: \"\"}\n  log: {level: warn}", 1)
	writeFile(t, path, next)
	resp, err := gotwampv1.NewDaemonServiceClient(r.conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(resp.GetSessionsAdded(), resp.GetSessionsRemoved(), resp.GetSessionsUpdated(), resp.GetSessionsUnchanged()) != "[new] [drop] [change] 1" {
		t.Errorf("reload = %v", resp)
	}
	// The log level applies at once.
	if r.level.Level() != slog.LevelWarn {
		t.Error("daemon.log.level warn was not applied")
	}

	// The long run completes all 500 packets: the reload did not abort it.
	ev, err := watch.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if s := ev.GetSummary(); s == nil || s.GetAborted() || s.GetSent() != 500 {
		t.Errorf("long run summary = %v; want 500 packets, not aborted", ev)
	}

	// An invalid file changes nothing.
	writeFile(t, path, "version: 9\n")
	if _, err := gotwampv1.NewDaemonServiceClient(r.conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("reload(invalid) = %v", err)
	}
	l, _ := client.ListSessions(t.Context(), &gotwampv1.ListSessionsRequest{})
	if len(l.GetSessions()) != 3 {
		t.Errorf("sessions after a failed reload = %d, want 3", len(l.GetSessions()))
	}
}

func TestMetricsAndJSONL(t *testing.T) {
	lp, cp := freePorts(t)
	dir := t.TempDir()
	body := "sender:\n  enabled: true\n  sessions:\n" + sessionYAML("full", "full", cp, ", labels: {site: lab}") +
		"export:\n  jsonl: {enabled: true, path: " + filepath.Join(dir, "results.jsonl") + "}\n"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := ln.Addr().String()
	_ = ln.Close()
	r, _ := sessionDaemonMetrics(t, metricsAddr, body, lp, cp)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	waitFor(t, "a run", func() bool {
		s, err := client.GetSession(t.Context(), &gotwampv1.GetSessionRequest{Name: "full"})
		return err == nil && s.GetSession().GetLastSummary() != nil
	})

	base := "http://" + r.d.MetricsAddr().String()
	metrics := httpGet(t, base+"/metrics")
	for _, want := range []string{
		`gotwamp_build_info{`, `gotwamp_sender_packets_sent_total{mode="full",session="full",site="lab",target="127.0.0.1"} 3`,
		fmt.Sprintf(`gotwamp_reflector_packets_reflected_total{kind="full",listener="127.0.0.1:%d"} 3`, cp), `gotwamp_clock_synchronized 1`,
		`gotwamp_sender_control_setup_seconds_count{`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	if got := httpGet(t, base+"/healthz"); !strings.HasPrefix(got, "ok") {
		t.Errorf("/healthz = %q", got)
	}

	r.stop(t) // flushes the JSON Lines file
	f, err := os.Open(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var found bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		if rec["type"] == "run_summary" && rec["session"] == "full" {
			sid, _ := rec["sid"].(string)
			if b, err := hex.DecodeString(sid); err != nil || len(b) != 16 {
				t.Errorf("run_summary sid = %q; want the Full session's SID", rec["sid"])
			}
			found = true
		}
	}
	if !found {
		t.Error("no run_summary of session full in the JSON Lines file")
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // a test URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCreateSessionRace: of concurrent creations of one name, one wins and
// the others get AlreadyExists.
func TestCreateSessionRace(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	spec := &gotwampv1.SessionSpec{
		Name:     "race",
		Spec:     &gotwampv1.TestSpec{Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_LIGHT, ReceiverPort: proto.Uint32(9)},
		Schedule: &gotwampv1.Schedule{Frequency: durationpb.New(time.Hour)},
	}
	const n = 8
	codesSeen := make(chan codes.Code, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_, err := client.CreateSession(t.Context(), &gotwampv1.CreateSessionRequest{Spec: spec})
			codesSeen <- status.Code(err)
		})
	}
	wg.Wait()
	close(codesSeen)
	ok := 0
	for c := range codesSeen {
		switch c {
		case codes.OK:
			ok++
		case codes.AlreadyExists:
		default:
			t.Errorf("CreateSession = %v, want OK or AlreadyExists", c)
		}
	}
	if ok != 1 {
		t.Errorf("%d creations succeeded, want 1", ok)
	}
}

// TestResultsKeepTheirSpec: a result carries the TestSpec it was measured
// with, not the session's definition of the moment (Codex M4), in
// GetSessionStats as in a watch opened before the update.
func TestResultsKeepTheirSpec(t *testing.T) {
	lp, cp := freePorts(t)
	r, _ := sessionDaemon(t, "sender:\n  enabled: true\n", lp, cp)
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	ctx := t.Context()
	watch, err := client.WatchResults(ctx, &gotwampv1.WatchResultsRequest{Sessions: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	api := &gotwampv1.SessionSpec{
		Name: "api",
		Spec: &gotwampv1.TestSpec{
			Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_LIGHT, ReceiverPort: proto.Uint32(uint32(lp)),
			Count: proto.Uint32(3), Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(200 * time.Millisecond),
			Padding: proto.Uint32(40),
		},
		Schedule: &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC, Frequency: durationpb.New(2 * time.Second)},
	}
	if _, err := client.CreateSession(ctx, &gotwampv1.CreateSessionRequest{Spec: api}); err != nil {
		t.Fatal(err)
	}
	// nextPadding reads the watch up to the next summary.
	nextPadding := func() uint32 {
		t.Helper()
		for {
			ev, err := watch.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if s := ev.GetSummary(); s != nil {
				return s.GetSpec().GetPadding()
			}
		}
	}
	if p := nextPadding(); p != 40 {
		t.Errorf("first summary padding = %d, want 40", p)
	}
	api.Spec.Padding = proto.Uint32(64)
	if _, err := client.UpdateSession(ctx, &gotwampv1.UpdateSessionRequest{Spec: api}); err != nil {
		t.Fatal(err)
	}
	if p := nextPadding(); p != 64 {
		t.Errorf("watched summary after the update: padding = %d, want 64", p)
	}
	st, err := client.GetSessionStats(ctx, &gotwampv1.GetSessionStatsRequest{Name: "api"})
	if err != nil || len(st.GetSummaries()) < 2 {
		t.Fatalf("GetSessionStats = %v, %v", st, err)
	}
	if first, last := st.GetSummaries()[0].GetSpec().GetPadding(), st.GetSummaries()[len(st.GetSummaries())-1].GetSpec().GetPadding(); first != 40 || last != 64 {
		t.Errorf("stored summaries: padding %d then %d, want 40 then 64", first, last)
	}
	s, err := client.GetSession(ctx, &gotwampv1.GetSessionRequest{Name: "api"})
	if err != nil || s.GetSession().GetLastSummary().GetSpec().GetPadding() != 64 {
		t.Errorf("GetSession = %v, %v", s, err)
	}
}

// TestReloadFreesRemovedSessions: a session a reload removes loses its
// results and its metrics, as one DeleteSession removes does (Codex M7).
func TestReloadFreesRemovedSessions(t *testing.T) {
	lp, cp := freePorts(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := ln.Addr().String()
	_ = ln.Close()
	r, path := sessionDaemonMetrics(t, metricsAddr, "sender:\n  enabled: true\n  sessions:\n"+sessionYAML("old", "light", lp, ""), lp, cp)
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	waitFor(t, "a run of old", func() bool {
		s, err := client.GetSession(t.Context(), &gotwampv1.GetSessionRequest{Name: "old"})
		return err == nil && s.GetSession().GetLastSummary() != nil
	})
	base := "http://" + r.d.MetricsAddr().String()
	if !strings.Contains(httpGet(t, base+"/metrics"), `session="old"`) {
		t.Fatal("/metrics lacks session old before the reload")
	}

	body, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(body), sessionYAML("old", "light", lp, ""), sessionYAML("new", "light", lp, ""), 1))
	if _, err := gotwampv1.NewDaemonServiceClient(r.conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSessionStats(t.Context(), &gotwampv1.GetSessionStatsRequest{Name: "old"}); status.Code(err) != codes.NotFound {
		t.Errorf("GetSessionStats(old) after the reload = %v, want NotFound", err)
	}
	if strings.Contains(httpGet(t, base+"/metrics"), `session="old"`) {
		t.Error("/metrics still has session old after the reload")
	}
}

// TestMetricsWithoutReflector: gotwamp_timestamping_mode is exported
// without the reflector role (B22), and /healthz follows the API (B26).
func TestMetricsWithoutReflector(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.Reflector.Enabled = false
	c.Daemon.Metrics.Listen = ln.Addr().String()
	_ = ln.Close()
	r := start(t, c, Options{})
	defer r.stop(t)
	base := "http://" + r.d.MetricsAddr().String()
	if m := httpGet(t, base+"/metrics"); !strings.Contains(m, `gotwamp_timestamping_mode{mode="`) {
		t.Error("/metrics lacks gotwamp_timestamping_mode")
	}
	if got := httpGet(t, base+"/healthz"); !strings.HasPrefix(got, "ok") {
		t.Errorf("/healthz = %q, want ok", got)
	}
	// With the API socket gone, the daemon no longer answers on it.
	if err := os.Remove(c.Daemon.API.Socket); err != nil {
		t.Fatal(err)
	}
	if got := httpGet(t, base+"/healthz"); strings.HasPrefix(got, "ok") {
		t.Errorf("/healthz without the API = %q, want an error", got)
	}
}

// TestReloadLogsWarnings: what a reload cannot apply is logged, since a
// SIGHUP has no one to answer (B27).
func TestReloadLogsWarnings(t *testing.T) {
	lp, cp := freePorts(t)
	r, path := sessionDaemon(t, "sender:\n  enabled: true\n", lp, cp)
	defer r.stop(t)
	body, _ := os.ReadFile(path)
	writeFile(t, path, strings.Replace(string(body), fmt.Sprintf(`listen: "127.0.0.1:%d"`, lp), fmt.Sprintf(`listen: "127.0.0.1:%d"`, lp+1), 1))
	if _, err := gotwampv1.NewDaemonServiceClient(r.conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{}); err != nil {
		t.Fatal(err)
	}
	if logs := r.logs.String(); !strings.Contains(logs, "reload: restart gotwampd to apply changes of reflector") {
		t.Errorf("no warning of the listener change in the log:\n%s", logs)
	}
}
