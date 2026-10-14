//go:build linux

//declscope:namespace daemon // shutdown tests that need a FIFO

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// TestShutdownWithStalledJSONL: with the JSON Lines output a FIFO whose
// reader never reads, gotwampd still stops within 5 s (NFR-05), drops the
// records left and says so (Codex re-review M9).
func TestShutdownWithStalledJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A reader that holds the FIFO open without reading lets the daemon
	// open it for writing, then lets its writes block.
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	c := testConfig(t)
	c.Export.JSONL.Enabled, c.Export.JSONL.Path = true, path
	r := start(t, c, Options{})
	info := &store.SessionInfo{Name: "s", Target: "192.0.2.1", Mode: "light"}
	for range 1000 {
		r.d.jsonl.Publish(store.Event{Kind: store.EventRunSummary, Time: time.Now(), Session: info, RunID: "r", Summary: &sender.Summary{}})
	}

	begin := time.Now()
	r.cancel()
	waited := make(chan error, 1)
	go func() { waited <- r.d.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("Wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gotwampd did not stop within 5 s with the JSON Lines output stalled")
	}
	t.Logf("stopped in %v", time.Since(begin))
	if !strings.Contains(r.logs.String(), "JSON Lines output did not take every record in time") {
		t.Errorf("no warning of the dropped records:\n%s", r.logs.String())
	}
}
