package clock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeSource returns whatever the test last set, and counts the reads.
type fakeSource struct {
	mu     sync.Mutex
	synced bool
	maxErr time.Duration
	err    error
	reads  int
}

func (f *fakeSource) set(synced bool, maxErr time.Duration, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced, f.maxErr, f.err = synced, maxErr, err
}

func (f *fakeSource) read() (bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.synced, f.maxErr, f.err
}

func (f *fakeSource) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

func checkStatus(t *testing.T, m *Monitor, wantSynced bool, wantMaxErr time.Duration) {
	t.Helper()
	synced, maxErr := m.Status()
	if synced != wantSynced || maxErr != wantMaxErr {
		t.Errorf("Status() = %v, %v; want %v, %v", synced, maxErr, wantSynced, wantMaxErr)
	}
}

func TestMonitorPollsEverySecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		src.set(true, 250*time.Microsecond, nil)
		m := newMonitor(Config{}, src)
		checkStatus(t, m, false, maxErrorLimit) // not started

		ctx, cancel := context.WithCancel(t.Context())
		m.Start(ctx)
		checkStatus(t, m, true, 250*time.Microsecond) // read synchronously

		src.set(false, 3*time.Millisecond, nil)
		time.Sleep(999 * time.Millisecond)
		synctest.Wait()
		checkStatus(t, m, true, 250*time.Microsecond) // next poll not due yet
		time.Sleep(time.Millisecond)
		synctest.Wait()
		checkStatus(t, m, false, 3*time.Millisecond)

		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := src.readCount(); got != 12 {
			t.Errorf("reads after 11 s = %d, want 12", got)
		}

		cancel()
		synctest.Wait() // the poller exits
		time.Sleep(5 * time.Second)
		if got := src.readCount(); got != 12 {
			t.Errorf("reads after cancel = %d, want 12", got)
		}
	})
}

func TestMonitorStartOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		m := newMonitor(Config{}, src)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m.Start(ctx)
		m.Start(ctx)
		time.Sleep(time.Second)
		synctest.Wait()
		if got := src.readCount(); got != 2 {
			t.Errorf("reads = %d, want 2 (one poller)", got)
		}
	})
}

func TestMonitorReadError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		src.set(true, time.Millisecond, errors.New("boom"))
		var logs bytes.Buffer
		m := newMonitor(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, src)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m.Start(ctx)
		checkStatus(t, m, false, maxErrorLimit)
		if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "boom") {
			t.Errorf("log = %q, want a warning with the error", logs.String())
		}

		src.set(true, time.Millisecond, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		checkStatus(t, m, true, time.Millisecond)
	})
}

func TestMonitorClampsMaxError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		src.set(true, time.Hour, nil)
		m := newMonitor(Config{}, src)
		m.Start(ctx)
		checkStatus(t, m, true, maxErrorLimit)

		src.set(true, -time.Second, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		checkStatus(t, m, true, 0)
	})
}

func TestMonitorLogsStateChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		src.set(true, time.Millisecond, nil)
		var logs bytes.Buffer
		m := newMonitor(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, src)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m.Start(ctx)
		src.set(true, 2*time.Millisecond, nil) // max error only: not logged
		time.Sleep(time.Second)
		synctest.Wait()
		src.set(false, 2*time.Millisecond, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		if n := strings.Count(logs.String(), "clock synchronization state"); n != 2 {
			t.Errorf("logged %d state records, want 2:\n%s", n, logs.String())
		}
	})
}

func TestMonitorOverrides(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		wantSynced bool
		wantMaxErr time.Duration
		wantReads  int
	}{
		{"auto", Config{}, false, 7 * time.Millisecond, 1},
		{"synced", Config{Sync: SyncSynced}, true, 7 * time.Millisecond, 1},
		{"unsynced", Config{Sync: SyncUnsynced}, false, 7 * time.Millisecond, 1},
		{"max error", Config{MaxError: 40 * time.Microsecond}, false, 40 * time.Microsecond, 1},
		{"both", Config{Sync: SyncSynced, MaxError: 40 * time.Microsecond}, true, 40 * time.Microsecond, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				src := &fakeSource{}
				src.set(false, 7*time.Millisecond, nil)
				m := newMonitor(tc.cfg, src)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				m.Start(ctx)
				checkStatus(t, m, tc.wantSynced, tc.wantMaxErr)
				if got := src.readCount(); got != tc.wantReads {
					t.Errorf("reads = %d, want %d", got, tc.wantReads)
				}
			})
		})
	}
}

func TestSyncNames(t *testing.T) {
	for _, s := range []Sync{SyncAuto, SyncSynced, SyncUnsynced} {
		got, err := ParseSync(s.String())
		if err != nil || got != s {
			t.Errorf("ParseSync(%q) = %v, %v; want %v", s.String(), got, err, s)
		}
	}
	if got, err := ParseSync(""); err != nil || got != SyncAuto {
		t.Errorf(`ParseSync("") = %v, %v; want auto`, got, err)
	}
	if _, err := ParseSync("maybe"); err == nil {
		t.Error(`ParseSync("maybe") succeeded, want an error`)
	}
	if got := Sync(9).String(); got != "Sync(9)" {
		t.Errorf("Sync(9).String() = %q", got)
	}
}

func TestNewMonitorUsesSystemSource(t *testing.T) {
	m := NewMonitor(Config{Sync: SyncSynced, MaxError: time.Millisecond})
	m.Start(t.Context()) // fully overridden: nothing is read
	checkStatus(t, m, true, time.Millisecond)
}

// TestMonitorLogsReadFailures: a failure to read the clock state is logged
// once when it starts, even when the clock was already unsynchronized, and
// its end is logged too (maintenance review A).
func TestMonitorLogsReadFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		src := &fakeSource{}
		src.set(false, time.Millisecond, nil)
		var logs bytes.Buffer
		m := newMonitor(Config{Logger: slog.New(slog.NewTextHandler(&logs, nil))}, src)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		m.Start(ctx)
		src.set(false, 0, errors.New("adjtimex: boom"))
		time.Sleep(3 * time.Second) // three failed reads
		synctest.Wait()
		src.set(false, time.Millisecond, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		out := logs.String()
		if strings.Count(out, "cannot read the clock state") != 1 || strings.Count(out, "clock state readable again") != 1 {
			t.Errorf("logs:\n%s", out)
		}
	})
}
