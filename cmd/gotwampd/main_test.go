package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"

	"github.com/Akkanbe/gotwamp/internal/config"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run(t.Context(), []string{"--version"}, &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "gotwampd ") || !strings.Contains(got, runtime.Version()) {
		t.Errorf("output %q", got)
	}
	// The linker's values win over the module's.
	defer func(v, c string) { version, commit = v, c }(version, commit)
	version, commit = "v1.2.3", "abc1234"
	out.Reset()
	if code := run(t.Context(), []string{"--version"}, &out, &out); code != 0 || !strings.HasPrefix(out.String(), "gotwampd v1.2.3 (commit abc1234, ") {
		t.Errorf("linked version: exit %d, %q", code, out.String())
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExitStatus(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"invalid config", []string{"--config", writeConfig(t, "version: 7\n")}, 1},
		{"bad log level", []string{"--config", writeConfig(t, ""), "--log-level", "loud"}, 1},
		{"unknown flag", []string{"--frobnicate"}, 1},
		{"socket directory is a file", []string{"--config", writeConfig(t,
			"daemon: {api: {socket: "+filepath.Join(writeConfig(t, ""), "d.sock")+"}, metrics: {listen: \"\"}}\n"+
				"reflector: {light: {listeners: [{listen: \"127.0.0.1:0\"}]}, control: {listen: [\"127.0.0.1:0\"]}}\n")}, 2},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if code := run(t.Context(), tc.args, &out, &out); code != tc.want {
			t.Errorf("%s: exit %d, want %d\n%s", tc.name, code, tc.want, out.String())
		}
	}
}

// TestMissingExplicitConfig: a --config naming no file runs on the
// defaults with a warning instead of exiting with a configuration error,
// which would loop under systemd (B10).
func TestMissingExplicitConfig(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // stop at once: the defaults bind privileged ports
	var out bytes.Buffer
	code := run(ctx, []string{"--config", filepath.Join(t.TempDir(), "none.yaml")}, &out, &out)
	if code == 1 || !strings.Contains(out.String(), "configuration file not found") {
		t.Errorf("exit %d, want no configuration error\n%s", code, out.String())
	}
}

// TestMissingDefaultConfig: without --config and a file at the default
// path, gotwampd runs on the defaults and still reloads that path, so that
// a file created there later applies on SIGHUP.
func TestMissingDefaultConfig(t *testing.T) {
	if _, err := os.Stat(config.DefaultPath); err == nil {
		t.Skipf("%s exists on this host", config.DefaultPath)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	code := run(ctx, nil, &out, &out)
	if code == 1 || !strings.Contains(out.String(), "until it exists and is reloaded") {
		t.Errorf("exit %d, want the defaults with the default path kept\n%s", code, out.String())
	}
}

func TestRunAndStop(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	path := writeConfig(t, "daemon: {api: {socket: "+sock+"}, metrics: {listen: \"\"}}\nreflector: {light: {listeners: [{listen: \"127.0.0.1:0\"}]}, control: {listen: [\"127.0.0.1:0\"]}}\n")
	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	done := make(chan int)
	go func() { done <- run(ctx, []string{"--config", path, "--log-format", "text"}, &out, &out) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the API socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gotwampd did not stop")
	}
}

// TestLogFormatFlagSurvivesReload: --log-format overrides the logger only,
// so that a reload of an unchanged file reports nothing to restart for.
func TestLogFormatFlagSurvivesReload(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	path := writeConfig(t, "daemon: {api: {socket: "+sock+"}, metrics: {listen: \"\"}, log: {format: json}}\n"+
		"reflector: {light: {listeners: [{listen: \"127.0.0.1:0\"}]}, control: {listen: [\"127.0.0.1:0\"]}}\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--config", path, "--log-format", "text"}, &out, &out) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the API socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	resp, err := gotwampv1.NewDaemonServiceClient(conn).ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWarnings()) != 0 {
		t.Errorf("reload warnings %v, want none", resp.GetWarnings())
	}
	if !strings.Contains(out.String(), "level=INFO") {
		t.Errorf("logs are not text:\n%s", out.String())
	}
	cancel()
	<-done
}

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
