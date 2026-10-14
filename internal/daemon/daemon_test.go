package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestStartStop(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	fi, err := os.Stat(c.Daemon.API.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o660 {
		t.Errorf("socket mode = %v, want a socket with 0660", fi.Mode())
	}
	if addrs := r.d.LightAddrs(); len(addrs) != 1 || addrs[0].Port() == 0 {
		t.Errorf("LightAddrs = %v", addrs)
	}
	begin := time.Now()
	r.stop(t)
	if time.Since(begin) > 5*time.Second {
		t.Errorf("shutdown took %v, want under 5s", time.Since(begin))
	}
	if _, err := os.Stat(c.Daemon.API.Socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket left behind: %v", err)
	}
	for _, want := range []string{"gotwampd started", "light reflector listening", "gotwampd stopped"} {
		if !strings.Contains(r.logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, r.logs.String())
		}
	}
}

func TestReflectorDisabled(t *testing.T) {
	c := testConfig(t)
	c.Reflector.Enabled = false
	r := start(t, c, Options{})
	defer r.stop(t)
	if r.d.LightAddrs() != nil {
		t.Error("LightAddrs should be nil without the reflector role")
	}
	resp, err := gotwampv1.NewReflectorServiceClient(r.conn).GetReflectorStatus(t.Context(), &gotwampv1.GetReflectorStatusRequest{})
	if err != nil || resp.GetEnabled() {
		t.Errorf("GetReflectorStatus = %v, %v; want disabled", resp, err)
	}
	flows, err := gotwampv1.NewReflectorServiceClient(r.conn).ListReflectorSessions(t.Context(), &gotwampv1.ListReflectorSessionsRequest{})
	if err != nil || len(flows.GetLightFlows()) != 0 {
		t.Errorf("ListReflectorSessions = %v, %v", flows, err)
	}
	st, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(t.Context(), &gotwampv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Without a reflector socket, a probe socket tells the mode (B22).
	if st.GetTimestampingMode() == gotwampv1.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED {
		t.Errorf("timestamping = %v, want the mode a socket gets", st.GetTimestampingMode())
	}
}

func TestGetStatus(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{ConfigPath: "/etc/gotwamp/test.yaml"})
	defer r.stop(t)
	st, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(t.Context(), &gotwampv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if st.GetVersion() != "v0.0.0-test" || st.GetCommit() != "abc1234" || !strings.HasPrefix(st.GetGoVersion(), "go") {
		t.Errorf("build info = %q %q %q", st.GetVersion(), st.GetCommit(), st.GetGoVersion())
	}
	if !st.GetSenderEnabled() || !st.GetReflectorEnabled() || st.GetConfigPath() != "/etc/gotwamp/test.yaml" {
		t.Errorf("status = %v", st)
	}
	// The mode the reflector's socket actually got (kernel where the
	// platform has SO_TIMESTAMPING, userspace otherwise).
	if mode, _ := r.d.refl.timestamping(); st.GetTimestampingMode() != protoTimestamping(mode) ||
		st.GetTimestampingMode() == gotwampv1.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED {
		t.Errorf("timestamping = %v, want the socket's %v", st.GetTimestampingMode(), mode)
	}
	clk := st.GetClock()
	if !clk.GetSynchronized() || clk.GetMaxError().AsDuration() != 100*time.Microsecond || clk.GetSource() != gotwampv1.ClockSyncSource_CLOCK_SYNC_SOURCE_CONFIG {
		t.Errorf("clock = %v", clk)
	}
	if st.GetUptime().AsDuration() < 0 || st.GetStartedAt().AsTime().After(time.Now()) {
		t.Errorf("uptime %v, started %v", st.GetUptime(), st.GetStartedAt())
	}
}

// reflect sends one TWAMP-Test packet to the Light reflector and returns the
// decoded reply.
func reflectOnce(t *testing.T, to netip.AddrPort) wire.ReflectorPacket {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p := wire.SenderPacket{Seq: 7, Timestamp: ntptime.FromTime(time.Now()), ErrorEstimate: errest.Compute(time.Millisecond, true), Padding: make([]byte, 27)}
	if _, err := c.WriteToUDPAddrPort(p.Append(nil), to); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, _, err := c.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatal(err)
	}
	var r wire.ReflectorPacket
	if err := r.Decode(buf[:n]); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReflectorService(t *testing.T) {
	c := testConfig(t)
	r := start(t, c, Options{})
	defer r.stop(t)
	addr := r.d.LightAddrs()[0]
	reply := reflectOnce(t, addr)
	if reply.SenderSeq != 7 || !reply.ErrorEstimate.Synchronized() {
		t.Errorf("reply = %+v; want the sender seq echoed and a synchronized estimate", reply)
	}

	client := gotwampv1.NewReflectorServiceClient(r.conn)
	st, err := client.GetReflectorStatus(t.Context(), &gotwampv1.GetReflectorStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.GetEnabled() || len(st.GetListeners()) != 2 || st.GetListeners()[0].GetAddress() != addr.String() ||
		st.GetListeners()[1].GetKind() != gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL ||
		st.GetListeners()[1].GetAddress() != r.d.ControlAddrs()[0].String() ||
		st.GetListeners()[0].GetPacketsReflected() != 1 || st.GetLightFlowsActive() != 1 {
		t.Errorf("status = %v", st)
	}
	if cs := st.GetCounters(); len(cs) != 2 || cs[0].GetPacketsReceived() != 1 || len(cs[0].GetDropped()) == 0 ||
		cs[1].GetKind() != gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL ||
		cs[0].GetDropped()[0].GetReason() != gotwampv1.DropReason_DROP_REASON_ACL {
		t.Errorf("counters = %v", cs)
	}

	flows, err := client.ListReflectorSessions(t.Context(), &gotwampv1.ListReflectorSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if lf := flows.GetLightFlows(); len(lf) != 1 || lf[0].GetDestination().GetPort() != uint32(addr.Port()) ||
		lf[0].GetNextSeq() != 1 || lf[0].GetPacketsReflected() != 1 {
		t.Errorf("flows = %v", lf)
	}
	full, err := client.ListReflectorSessions(t.Context(), &gotwampv1.ListReflectorSessionsRequest{Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL})
	if err != nil || len(full.GetLightFlows())+len(full.GetFullSessions()) != 0 {
		t.Errorf("FULL listing = %v, %v; want empty", full, err)
	}
}

func TestReloadConfig(t *testing.T) {
	c := testConfig(t)
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	if err := os.WriteFile(path, []byte("sender: {defaults: {padding: 1500}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := start(t, c, Options{ConfigPath: path})
	defer r.stop(t)
	client := gotwampv1.NewDaemonServiceClient(r.conn)
	resp, err := client.ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(resp.GetWarnings(), "\n"); !strings.Contains(w, "sender.defaults.padding") || !strings.Contains(w, "restart gotwampd to apply changes of") {
		t.Errorf("warnings = %q", w)
	}

	if err := os.WriteFile(path, []byte("version: 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("ReloadConfig(invalid) = %v, want InvalidArgument", err)
	}
	// The daemon's log tells the failure too, not only the client.
	if n := strings.Count(r.logs.String(), "reload failed"); n != 1 {
		t.Errorf("a failed ReloadConfig logged %d failures, want 1:\n%s", n, r.logs.String())
	}

	// SIGHUP takes the same path; the failure is logged, the daemon lives.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(r.logs.String(), "reload failed") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(r.logs.String(), "reload failed") != 2 {
		t.Errorf("SIGHUP did not reload:\n%s", r.logs.String())
	}
}

// TestReloadFixedLogLevel: a level the command line fixed (LogLevel nil)
// outlasts a reload, which says so instead of applying the file's.
func TestReloadFixedLogLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	if err := os.WriteFile(path, []byte("daemon: {log: {level: error}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cur := config.Default()
	cur.Daemon.Log.Level = "debug" // as gotwampd --log-level debug sets it
	var logs syncBuffer
	for _, fixed := range []bool{true, false} {
		live := &liveConfig{}
		live.p.Store(cur)
		r := &reloader{path: path, cfg: live, drop: func(string) {}, log: slog.New(slog.NewTextHandler(&logs, nil))}
		var lv slog.LevelVar
		lv.Set(slog.LevelDebug)
		if !fixed {
			r.level = &lv
		}
		resp, err := r.reload()
		if err != nil {
			t.Fatal(err)
		}
		warned := strings.Contains(strings.Join(resp.GetWarnings(), "\n"), "fixed by gotwampd --log-level")
		switch {
		case fixed && (!warned || live.get().Daemon.Log.Level != "debug"):
			t.Errorf("fixed: level %q, warnings %q", live.get().Daemon.Log.Level, resp.GetWarnings())
		case !fixed && (warned || lv.Level() != slog.LevelError || live.get().Daemon.Log.Level != "error"):
			t.Errorf("not fixed: level %v (%q), warnings %q", lv.Level(), live.get().Daemon.Log.Level, resp.GetWarnings())
		}
	}
}

func TestReloadWithoutFile(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	_, err := gotwampv1.NewDaemonServiceClient(r.conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("ReloadConfig = %v, want FailedPrecondition", err)
	}
}

func TestSocketConflicts(t *testing.T) {
	t.Run("stale socket is replaced", func(t *testing.T) {
		c := testConfig(t)
		ln, err := net.Listen("unix", c.Daemon.API.Socket)
		if err != nil {
			t.Fatal(err)
		}
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = ln.Close() // leaves the file, like a crashed daemon
		start(t, c, Options{}).stop(t)
	})
	t.Run("live socket", func(t *testing.T) {
		c := testConfig(t)
		r := start(t, c, Options{})
		defer r.stop(t)
		c2 := *c
		c2.Reflector.Enabled = false
		_, err := Start(t.Context(), &c2, Options{})
		if !errors.Is(err, ErrStartup) || !strings.Contains(err.Error(), "in use") {
			t.Errorf("second Start = %v, want ErrStartup (in use)", err)
		}
	})
	t.Run("not a socket", func(t *testing.T) {
		c := testConfig(t)
		if err := os.WriteFile(c.Daemon.API.Socket, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Start(t.Context(), c, Options{})
		if !errors.Is(err, ErrStartup) || !strings.Contains(err.Error(), "not a socket") {
			t.Errorf("Start = %v, want ErrStartup (not a socket)", err)
		}
	})
	t.Run("missing directory is created", func(t *testing.T) {
		c := testConfig(t)
		c.Daemon.API.Socket = filepath.Join(t.TempDir(), "run", "gotwamp", "d.sock")
		r := start(t, c, Options{})
		defer r.stop(t)
		fi, err := os.Stat(filepath.Dir(c.Daemon.API.Socket))
		if err != nil || !fi.IsDir() || fi.Mode().Perm()&^0o750 != 0 {
			t.Errorf("socket directory: %v, %v; want a directory of at most 0750", fi.Mode(), err)
		}
		if !strings.Contains(r.logs.String(), "created the api socket directory") {
			t.Errorf("no log of the directory creation:\n%s", r.logs.String())
		}
	})
	t.Run("parent is a file", func(t *testing.T) {
		c := testConfig(t)
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		c.Daemon.API.Socket = filepath.Join(file, "d.sock")
		if _, err := Start(t.Context(), c, Options{}); !errors.Is(err, ErrStartup) {
			t.Errorf("Start = %v, want ErrStartup", err)
		}
	})
	t.Run("world-writable directory warns", func(t *testing.T) {
		c := testConfig(t)
		dir := filepath.Join(t.TempDir(), "open")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		c.Daemon.API.Socket = filepath.Join(dir, "d.sock")
		r := start(t, c, Options{})
		defer r.stop(t)
		if !strings.Contains(r.logs.String(), "writable by every user without the sticky bit") {
			t.Errorf("no warning:\n%s", r.logs.String())
		}
	})
	t.Run("unknown group", func(t *testing.T) {
		c := testConfig(t)
		c.Daemon.API.SocketGroup = "no-such-group-gotwamp"
		if _, err := Start(t.Context(), c, Options{}); !errors.Is(err, ErrStartup) {
			t.Errorf("Start = %v, want ErrStartup", err)
		}
	})
	t.Run("own group", func(t *testing.T) {
		c := testConfig(t)
		g := groupName(t)
		c.Daemon.API.SocketGroup = g
		start(t, c, Options{}).stop(t)
	})
	t.Run("light port in use", func(t *testing.T) {
		busy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer busy.Close()
		c := testConfig(t)
		c.Reflector.Light.Listeners = []config.LightListener{{Listen: busy.LocalAddr().String()}}
		if _, err := Start(t.Context(), c, Options{}); !errors.Is(err, ErrStartup) {
			t.Errorf("Start = %v, want ErrStartup", err)
		}
		// The API socket was never created.
		if _, err := os.Stat(c.Daemon.API.Socket); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("socket exists after a failed start: %v", err)
		}
	})
}

func TestNewLogger(t *testing.T) {
	var b bytes.Buffer
	l, level, err := NewLogger(&b, "json", "warn")
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hidden")
	l.Warn("shown")
	level.Set(slog.LevelInfo)
	l.Info("now shown")
	if s := b.String(); strings.Contains(s, "hidden") || !strings.Contains(s, `"msg":"shown"`) || !strings.Contains(s, "now shown") {
		t.Errorf("json log = %q", s)
	}
	// Durations are text, in groups too, not nanoseconds.
	b.Reset()
	l.Info("timed", "max_error", 43500*time.Microsecond, slog.Group("g", "wait", 5*time.Second))
	if s := b.String(); !strings.Contains(s, `"max_error":"43.5ms"`) || !strings.Contains(s, `"wait":"5s"`) {
		t.Errorf("json durations = %q", s)
	}
	if _, _, err := NewLogger(io.Discard, "xml", "info"); err == nil {
		t.Error("format xml accepted")
	}
	if _, _, err := NewLogger(io.Discard, "text", "loud"); err == nil {
		t.Error("level loud accepted")
	}
}

func groupName(t *testing.T) string {
	t.Helper()
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skipf("primary group: %v", err)
	}
	return g.Name
}

// TestRuntimeFailureStopsDaemon: when a part fails after startup (here the
// API listener breaks), the daemon stops by itself instead of running on
// unreachable, and reports ErrRuntime.
func TestRuntimeFailureStopsDaemon(t *testing.T) {
	c := testConfig(t)
	logs := &syncBuffer{}
	log, _, _ := NewLogger(logs, "text", "info")
	d, err := Start(t.Context(), c, Options{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.sock.Close()
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrRuntime) {
			t.Errorf("Wait = %v, want ErrRuntime", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop after its API server failed")
	}
	if !strings.Contains(logs.String(), "gotwampd part failed; stopping") {
		t.Errorf("no error log:\n%s", logs.String())
	}
}

// TestWithin: a shutdown step that overruns its deadline is given up (B8).
func TestWithin(t *testing.T) {
	if !within(time.Now().Add(time.Second), func() {}) {
		t.Error("a quick step overran")
	}
	release := make(chan struct{})
	defer close(release)
	begin := time.Now()
	if within(time.Now().Add(50*time.Millisecond), func() { <-release }) {
		t.Error("a stuck step returned")
	}
	if d := time.Since(begin); d > time.Second {
		t.Errorf("within waited %v for a stuck step", d)
	}
}

// TestStatsSince: GetSessionStats with since reads the store's time range:
// with sqlite from the database, beyond the history in memory; with memory
// from that history. Without since, the history.
func TestStatsSince(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			c := testConfig(t)
			c.Daemon.Limits.ResultHistory = 2
			c.Store.Backend, c.Store.SQLite.Path = backend, filepath.Join(t.TempDir(), "results.db")
			r := start(t, c, Options{})
			defer r.stop(t)
			info := &store.SessionInfo{Name: "past", Target: "192.0.2.10", Mode: "light"}
			now := time.Now()
			for i := range 5 {
				var sum sender.Summary
				sum.Sent, sum.EndedAt = uint64(i+1), now.Add(time.Duration(i-5)*time.Hour)
				r.d.results.Publish(store.Event{Kind: store.EventRunSummary, Time: sum.EndedAt, Session: info, RunID: fmt.Sprint(i), Summary: &sum})
			}
			client := gotwampv1.NewSenderServiceClient(r.conn)
			sent := func(req *gotwampv1.GetSessionStatsRequest) []uint32 {
				t.Helper()
				resp, err := client.GetSessionStats(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				var out []uint32
				for _, s := range resp.GetSummaries() {
					out = append(out, s.GetSent())
				}
				return out
			}
			want := map[string][]uint32{"sqlite": {2, 3, 4, 5}, "memory": {4, 5}}[backend]
			if got := sent(&gotwampv1.GetSessionStatsRequest{Name: "past", Since: timestamppb.New(now.Add(-4*time.Hour - time.Minute))}); !slices.Equal(got, want) {
				t.Errorf("since 4h: %v, want %v", got, want)
			}
			if got := sent(&gotwampv1.GetSessionStatsRequest{Name: "past", Since: timestamppb.New(now.Add(-4*time.Hour - time.Minute)), Last: 1}); !slices.Equal(got, []uint32{5}) {
				t.Errorf("since 4h, last 1: %v", got)
			}
			if got := sent(&gotwampv1.GetSessionStatsRequest{Name: "past"}); !slices.Equal(got, []uint32{4, 5}) {
				t.Errorf("without since: %v, want the history", got)
			}
			if got := sent(&gotwampv1.GetSessionStatsRequest{Name: "past", Since: timestamppb.New(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))}); len(got) != len(map[string][]uint32{"sqlite": {1, 2, 3, 4, 5}, "memory": {4, 5}}[backend]) {
				t.Errorf("since year 1: %v", got)
			}
		})
	}
}
