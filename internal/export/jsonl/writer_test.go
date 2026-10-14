package jsonl

import (
	"bufio"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
)

var session = &store.SessionInfo{Name: "s"}

// failure returns a run error event whose record is exactly 130 octets.
func failure(i int) store.Event {
	msg := strings.Repeat("x", 2) + string(rune('a'+i%26))
	return store.Event{
		Kind:    store.EventRunError,
		Time:    time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		Session: session,
		RunID:   "r",
		Error:   &store.RunError{Stage: "test", Message: msg},
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestRecordSize(t *testing.T) {
	line, err := encode(failure(0), time.UTC)
	if err != nil || len(line) != 130 {
		t.Fatalf("record is %d octets: %s", len(line), line)
	}
}

func TestSizeRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	w, err := Open(Config{Path: path, MaxSize: 300, MaxFiles: 2, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	// Two 130-octet records fit in 300; the third rotates. Seven records
	// make files of 2, 2, 2 and 1, of which the oldest is dropped.
	for i := range 7 {
		w.Publish(failure(i))
		waitWritten(t, w, uint64(i+1))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{path: 1, path + ".1": 2, path + ".2": 2} {
		if got := readLines(t, name); len(got) != want {
			t.Errorf("%s has %d lines, want %d", filepath.Base(name), len(got), want)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("%s.3 exists: %v", path, err)
	}
	if got := readLines(t, path)[0]; !strings.Contains(got, `"message":"xxg"`) {
		t.Errorf("newest record %s", got)
	}
	if w.Written() != 7 || w.Dropped() != 0 {
		t.Errorf("written %d, dropped %d", w.Written(), w.Dropped())
	}
	// Publishing after Close drops.
	w.Publish(failure(0))
	if w.Dropped() != 1 {
		t.Errorf("dropped %d after Close", w.Dropped())
	}
}

func waitWritten(t *testing.T, w *Writer, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for w.Written() < n {
		if time.Now().After(deadline) {
			t.Fatalf("written %d, want %d", w.Written(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func TestDailyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	jst := time.FixedZone("JST", 9*3600)
	clock := &fakeClock{t: time.Date(2026, 10, 4, 23, 59, 0, 0, jst)}
	w, err := Open(Config{Path: path, Daily: true, MaxSize: -1, Location: jst, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	w.Publish(failure(0))
	w.Publish(failure(1))
	waitWritten(t, w, 2)
	clock.set(time.Date(2026, 10, 5, 0, 0, 1, 0, jst))
	w.Publish(failure(2))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if a, b := len(readLines(t, path+".1")), len(readLines(t, path)); a != 2 || b != 1 {
		t.Errorf("files hold %d and %d records", a, b)
	}

	// A file left from an earlier day rotates at the first record.
	old := time.Date(2026, 10, 3, 12, 0, 0, 0, jst)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	w, err = Open(Config{Path: path, Daily: true, Location: jst, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	w.Publish(failure(3))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if a, b, c := len(readLines(t, path+".2")), len(readLines(t, path+".1")), len(readLines(t, path)); a != 2 || b != 1 || c != 1 {
		t.Errorf("files hold %d, %d and %d records", a, b, c)
	}
}

func TestFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	w, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	w.Publish(store.Event{Kind: store.EventPacket, Session: session})
	w.Publish(store.Event{Kind: store.EventLost, Session: session})
	w.Publish(store.Event{Kind: store.EventRunSkipped, Session: session})
	w.Publish(failure(0))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if lines := readLines(t, path); len(lines) != 1 || w.Dropped() != 0 {
		t.Errorf("lines %v, dropped %d", lines, w.Dropped())
	}
}

func TestDropWhenFull(t *testing.T) {
	// No writer goroutine: the channel fills up.
	w := &Writer{cfg: Config{Records: DefaultRecords}, ch: make(chan store.Event, 1)}
	w.Publish(failure(0))
	w.Publish(failure(1))
	if w.Dropped() != 1 {
		t.Errorf("dropped %d", w.Dropped())
	}
}

func TestEncodeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	w, err := Open(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	// JSON has no NaN.
	bad := &sender.Summary{Summary: stats.Summary{LossRatio: math.NaN()}}
	w.Publish(store.Event{Kind: store.EventRunSummary, Session: session, Summary: bad})
	w.Publish(failure(0))
	err = w.Close()
	if lines := readLines(t, path); len(lines) != 1 || err == nil || w.Dropped() != 1 || w.Written() != 1 {
		t.Errorf("lines %d, err %v, dropped %d", len(lines), err, w.Dropped())
	}
}

func TestParseRecords(t *testing.T) {
	r, err := ParseRecords([]string{"run_summary", "window_summary", "run_error", "packet"})
	if err != nil || r != RunSummary|WindowSummary|RunError|Packet {
		t.Errorf("ParseRecords = %v, %v", r, err)
	}
	if _, err := ParseRecords([]string{"lost"}); err == nil {
		t.Error("unknown record type accepted")
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(Config{}); err == nil {
		t.Error("empty path accepted")
	}
	if _, err := Open(Config{Path: filepath.Join(t.TempDir(), "missing", "x.jsonl")}); err == nil {
		t.Error("missing directory accepted")
	}
}

func TestRotationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.jsonl")
	w, err := Open(Config{Path: path, MaxSize: 150, MaxFiles: 1})
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the rotated file goes makes the rename fail; the
	// writer keeps appending to the same file.
	if err := os.Mkdir(path+".1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".1", "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		w.Publish(failure(i))
	}
	err = w.Close()
	if err == nil || w.Err() == nil {
		t.Error("no error reported")
	}
	if lines := readLines(t, path); len(lines) != 3 {
		t.Errorf("%d lines kept", len(lines))
	}
	if !strings.Contains(w.Err().Error(), "rotate") {
		t.Errorf("error %v", w.Err())
	}
}

// BenchmarkPublish is what a session pays per record: the channel send. The
// encoding and writing happen on the writer goroutine.
func BenchmarkPublish(b *testing.B) {
	w, err := Open(Config{Path: filepath.Join(b.TempDir(), "results.jsonl"), Buffer: 1 << 16})
	if err != nil {
		b.Fatal(err)
	}
	e := failure(0)
	b.ReportAllocs()
	for b.Loop() {
		w.Publish(e)
	}
	b.StopTimer()
	if err := w.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(w.Dropped())/float64(b.N), "dropped/op")
}

// TestReopenAfterFailedRotation: when the new file cannot be opened after a
// rotation, records are dropped and counted until a later attempt opens it
// (review m6).
func TestReopenAfterFailedRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	w, err := Open(Config{Path: path, MaxSize: 200, Location: time.UTC, Now: clock.now})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	failing := true
	orig := openFile
	openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return nil, os.ErrPermission
		}
		return orig(name, flag, perm)
	}
	t.Cleanup(func() { openFile = orig })

	w.Publish(failure(0))
	waitWritten(t, w, 1)
	w.Publish(failure(1)) // rotates; the new file cannot be opened
	w.Publish(failure(2)) // within the retry delay
	waitFor(t, func() bool { return w.Dropped() == 2 })
	if w.Err() == nil {
		t.Error("no error while the file is missing")
	}

	mu.Lock()
	failing = false
	mu.Unlock()
	clock.set(clock.now().Add(2 * time.Second))
	w.Publish(failure(3))
	waitWritten(t, w, 2)
	if w.Err() != nil {
		t.Errorf("error after recovery: %v", w.Err())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if lines := readLines(t, path); len(lines) != 1 || !strings.Contains(lines[0], `"message":"xxd"`) {
		t.Errorf("new file holds %v", lines)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
	}
}

// TestRecoverAfterWriteError: a write that fails (a full disk, here a file
// that cannot be written) loses its records, is logged once, and the
// writer reopens the file after the retry delay instead of failing every
// later record (maintenance review A).
func TestRecoverAfterWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	broken := true
	orig := openFile
	openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		mu.Lock()
		defer mu.Unlock()
		if broken {
			f, err := orig(name, flag, perm) // create it
			if err != nil {
				return nil, err
			}
			_ = f.Close()
			return os.Open(name) // read only: every write fails
		}
		return orig(name, flag, perm)
	}
	t.Cleanup(func() { openFile = orig })
	var logs syncBuffer
	w, err := Open(Config{Path: path, Location: time.UTC, Now: clock.now, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}

	w.Publish(failure(0))
	waitFor(t, func() bool { return w.Dropped() == 1 })
	if w.Err() == nil {
		t.Error("no error after a failed write")
	}
	w.Publish(failure(1)) // within the retry delay: dropped
	waitFor(t, func() bool { return w.Dropped() == 2 })

	mu.Lock()
	broken = false
	mu.Unlock()
	clock.set(clock.now().Add(2 * time.Second))
	w.Publish(failure(2))
	waitWritten(t, w, 1)
	if err := w.Close(); err != nil {
		t.Errorf("Close after recovery: %v", err)
	}
	if lines := readLines(t, path); len(lines) != 1 || !strings.Contains(lines[0], `"message":"xxc"`) {
		t.Errorf("file holds %v", lines)
	}
	out := logs.String()
	if strings.Count(out, "JSON Lines output failing") != 1 || !strings.Contains(out, "JSON Lines output recovered") {
		t.Errorf("logs:\n%s", out)
	}
}

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
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
