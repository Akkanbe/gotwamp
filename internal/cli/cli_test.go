package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func TestStatus(t *testing.T) {
	h := newHarness(t)
	code, out, errs := h.run(t.Context(), "status")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	for _, want := range []string{
		"gotwampd      v0.2.0 (commit abc1234, go1.26.7)", "uptime        1h30m0s", "roles         reflector",
		"timestamping  userspace", "clock         synchronized, max error 250µs (kernel)", "config        (built-in defaults)",
		"store         sqlite /var/lib/gotwamp/results.db (12 results written, 1 dropped, 0 failed)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	code, out, _ = h.run(t.Context(), "status", "--json")
	var v map[string]any
	if code != ExitOK || json.Unmarshal([]byte(out), &v) != nil || v["version"] != "v0.2.0" || v["timestamping_mode"] != "TIMESTAMPING_MODE_USERSPACE" {
		t.Errorf("--json: exit %d, %s", code, out)
	}
	code, out, _ = h.run(t.Context(), "--output", "jsonl", "status")
	if code != ExitOK || strings.Count(out, "\n") != 1 {
		t.Errorf("jsonl: exit %d, %q", code, out)
	}
}

func TestUnavailable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nothing.sock")
	var errb bytes.Buffer
	code := Main(t.Context(), []string{"--socket", sock, "status", "--timeout", "2s"}, BuildInfo{}, Env{Stderr: &errb})
	if code != ExitUnavailable || !strings.Contains(errb.String(), "cannot connect to gotwampd at "+sock) {
		t.Errorf("exit %d: %s", code, errb.String())
	}
}

func TestSocketPaths(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	lis, err := net.Listen("unix", "d.sock")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	gotwampv1.RegisterDaemonServiceServer(srv, newFakeDaemon())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	for _, sock := range []string{"d.sock", "./d.sock", filepath.Join(dir, "d.sock")} {
		var out, errb bytes.Buffer
		code := Main(t.Context(), []string{"--socket", sock, "status", "--timeout", "5s"}, BuildInfo{},
			Env{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }})
		if code != ExitOK || !strings.Contains(out.String(), "uptime") {
			t.Errorf("--socket %s: exit %d: %s%s", sock, code, out.String(), errb.String())
		}
	}
}

func TestSocketFromEnvironment(t *testing.T) {
	var got string
	Main(t.Context(), []string{"status"}, BuildInfo{}, Env{
		Getenv: func(k string) string {
			if k == "GOTWAMP_SOCKET" {
				return "/tmp/from-env.sock"
			}
			return ""
		},
		Dial: func(s string) (*grpc.ClientConn, error) {
			got = s
			return nil, exitf(ExitUnavailable, "no")
		},
	})
	if got != "/tmp/from-env.sock" {
		t.Errorf("dialed %q", got)
	}
}

func TestVersionAndUsage(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run(t.Context(), "version"); code != ExitOK || !strings.HasPrefix(out, "gotwamp v0.2.0 (commit abc1234, go") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _ := h.run(t.Context(), "--version"); code != ExitOK || !strings.HasPrefix(out, "gotwamp v0.2.0") {
		t.Errorf("--version: %d %q", code, out)
	}
	if code, out, _ := h.run(t.Context(), "version", "--json"); code != ExitOK || !strings.Contains(out, `"commit": "abc1234"`) {
		t.Errorf("version --json: %d %q", code, out)
	}
	if code, out, _ := h.run(t.Context(), "version", "--output", "jsonl"); code != ExitOK || strings.Count(out, "\n") != 1 {
		t.Errorf("version jsonl: %d %q", code, out)
	}
	if code := Main(t.Context(), []string{"version"}, BuildInfo{}, Env{}); code != ExitOK {
		t.Errorf("version with an empty Env: %d", code)
	}
	// Without linked values, the JSON shows what the table shows.
	var out bytes.Buffer
	if code := Main(t.Context(), []string{"version", "--json"}, BuildInfo{}, Env{Stdout: &out}); code != ExitOK ||
		!strings.Contains(out.String(), `"version": "dev"`) || !strings.Contains(out.String(), `"commit": "unknown"`) {
		t.Errorf("version --json without build info: %d %q", code, out.String())
	}
	for _, args := range [][]string{{"frobnicate"}, {"status", "extra"}, {"--output", "xml", "status"}, {"ping"}, {"status", "--bogus"}} {
		if code, _, _ := h.run(t.Context(), args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

// TestRPCErrorMessages: failures gotwampd explains are shown in its words,
// without the gRPC code's name, and a timeout names the flag to raise.
func TestRPCErrorMessages(t *testing.T) {
	g := &globals{socket: "/run/x.sock", timeout: 3 * time.Second}
	cases := []struct {
		err  error
		code int
		want string
	}{
		{status.Error(codes.NotFound, `no session "x"`), ExitFailure, `no session "x"`},
		{status.Error(codes.FailedPrecondition, "the sender role is disabled"), ExitFailure, "the sender role is disabled"},
		{status.Error(codes.Aborted, "gotwampd is shutting down"), ExitFailure, "gotwampd is shutting down"},
		{status.Error(codes.DeadlineExceeded, "context deadline exceeded"), ExitFailure, "gotwampd did not answer within --rpc-timeout 3s: context deadline exceeded"},
		{status.Error(codes.Internal, "boom"), ExitFailure, "Internal: boom"},
		{status.Error(codes.InvalidArgument, "count 0"), ExitUsage, "count 0"},
		{status.Error(codes.Unavailable, "refused"), ExitUnavailable, "cannot connect to gotwampd at /run/x.sock: refused"},
	}
	for _, tc := range cases {
		var ee *exitError
		if err := g.rpcError(tc.err); !errors.As(err, &ee) || ee.code != tc.code || err.Error() != tc.want {
			t.Errorf("rpcError(%v) = %v (exit %v), want %q (exit %d)", tc.err, err, ee, tc.want, tc.code)
		}
	}
}
