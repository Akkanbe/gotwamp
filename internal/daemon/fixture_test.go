package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Akkanbe/gotwamp/internal/config"
)

// syncBuffer is a log sink safe for concurrent writes.
//
//declscope:package // the fixture of every daemon test file
type syncBuffer struct {
	//declscope:private
	mu sync.Mutex
	//declscope:private
	b bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns what was logged.
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testConfig is a configuration with both roles, a socket in a temporary
// directory and a Light reflector on a kernel-chosen loopback port.
//
//declscope:package // the fixture of every daemon test file
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Default()
	c.Sender.Enabled = true
	c.Daemon.API.Socket = filepath.Join(t.TempDir(), "gotwampd.sock")
	c.Daemon.Clock.Sync = "synced"
	c.Daemon.Clock.MaxError = 100 * time.Microsecond
	c.Daemon.Metrics.Listen = ""
	c.Reflector.Light.Listeners = []config.LightListener{{Listen: "127.0.0.1:0"}}
	c.Reflector.Control.Listen = []string{"127.0.0.1:0"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

// running is a started daemon and a client connected to it.
//
//declscope:package // the fixture of every daemon test file
type running struct {
	d    *Daemon
	conn *grpc.ClientConn
	logs *syncBuffer
	// level is the daemon's log level.
	level *slog.LevelVar
	// cancel stops the daemon without waiting for it.
	cancel context.CancelFunc
}

// stop stops the daemon and waits for it.
//
//declscope:package // the fixture of every daemon test file
func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	if err := r.d.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}
}

// start starts a daemon with c and connects a client.
//
//declscope:package // the fixture of every daemon test file
func start(t *testing.T, c *config.Config, opts Options) *running {
	t.Helper()
	logs := &syncBuffer{}
	opts.Logger, opts.LogLevel, _ = NewLogger(logs, "text", "debug")
	if opts.Version == "" {
		opts.Version, opts.Commit = "v0.0.0-test", "abc1234"
	}
	ctx, cancel := context.WithCancel(t.Context())
	d, err := Start(ctx, c, opts)
	if err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	conn, err := grpc.NewClient("unix://"+c.Daemon.API.Socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	r := &running{d: d, cancel: cancel, conn: conn, logs: logs, level: opts.LogLevel}
	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
	})
	return r
}

// waitFor polls cond until it holds or 10 s pass.
//
//declscope:package // the fixture of every daemon test file
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// writeFile writes a configuration file in a temporary directory.
//
//declscope:package // the fixture of every daemon test file
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
