//go:build unix

//declscope:namespace writer // the writer tests on a FIFO, with the helpers of writer_test.go

package jsonl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestShutdownBlockedOutput: with a reader that never reads, writes to a
// FIFO block; Shutdown still returns by its deadline, drops what was left
// and ends the writer goroutine (Codex re-review M9).
func TestShutdownBlockedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skip(err)
	}
	reader, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	w, err := Open(Config{Path: path, MaxSize: -1, Buffer: 8192})
	if err != nil {
		t.Fatal(err)
	}
	// About 650 kB of records: far more than a pipe and the buffer hold.
	for i := range 5000 {
		w.Publish(failure(i))
	}
	// The writer takes records until the pipe and its buffer are full, and
	// then blocks with the rest queued.
	waitFor(t, func() bool { return len(w.ch) < 5000 })
	time.Sleep(100 * time.Millisecond)
	if len(w.ch) == 0 {
		t.Fatal("the FIFO took every record; the test needs more")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = w.Shutdown(ctx)
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v", d)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v", err)
	}
	select {
	case <-w.done:
	default:
		t.Error("the writer goroutine is still running")
	}
	if w.Dropped() == 0 || w.Written()+w.Dropped() > 5000 {
		t.Errorf("written %d, dropped %d", w.Written(), w.Dropped())
	}
	// Later calls and publishes do not block either.
	if err := w.Shutdown(context.Background()); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Logf("second Shutdown: %v", err)
	}
	w.Publish(failure(0))
}

// TestShutdownInTime: an output that keeps up is written in full.
func TestShutdownInTime(t *testing.T) {
	w, err := Open(Config{Path: filepath.Join(t.TempDir(), "results.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		w.Publish(failure(i))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil || w.Written() != 100 {
		t.Errorf("Shutdown = %v, written %d", err, w.Written())
	}
}
