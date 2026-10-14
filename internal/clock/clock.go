// Package clock monitors the synchronization state and the maximum error of
// the local clock (SPEC §8.3). The values feed the Error Estimate of every
// TWAMP-Test packet (SPEC §8.4) and the daemon's status and metrics.
//
// On Linux the state is read with adjtimex(2) once a second: the clock is
// synchronized when the clock state is not TIME_ERROR and STA_UNSYNC is clear,
// and the maximum error is the kernel's maxerror. Both can be overridden by
// configuration because some environments never update the kernel state.
package clock

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// maxErrorLimit is the largest maximum error the kernel reports (NTP_PHASE_LIMIT,
// 16 s): the value of maxerror once no time source has updated the clock for a
// while. Status also reports it when the clock state cannot be read.
//
//declscope:package // the kernel reader (adjtimex_linux.go) clamps maxerror to it
const maxErrorLimit = 16 * time.Second

// pollInterval is how often the kernel state is read.
const pollInterval = time.Second

// Sync selects how the synchronization state is determined (clock.sync).
type Sync uint8

// The values of Sync, as ParseSync returns them.
//
//declscope:ignore overexported // an enum: its values document the type the daemon takes through ParseSync
const (
	// SyncAuto reads the state from the kernel.
	SyncAuto Sync = iota
	// SyncSynced always reports a synchronized clock.
	SyncSynced
	// SyncUnsynced always reports an unsynchronized clock.
	SyncUnsynced
)

// String returns the configuration name of s.
func (s Sync) String() string {
	switch s {
	case SyncAuto:
		return "auto"
	case SyncSynced:
		return "synced"
	case SyncUnsynced:
		return "unsynced"
	default:
		return fmt.Sprintf("Sync(%d)", uint8(s))
	}
}

// ParseSync parses a clock.sync configuration value.
func ParseSync(s string) (Sync, error) {
	switch s {
	case "auto", "":
		return SyncAuto, nil
	case "synced":
		return SyncSynced, nil
	case "unsynced":
		return SyncUnsynced, nil
	default:
		return 0, fmt.Errorf("clock: unknown sync mode %q (want auto, synced or unsynced)", s)
	}
}

// Config overrides what the kernel reports.
type Config struct {
	// Sync overrides the synchronization state unless it is SyncAuto.
	Sync Sync
	// MaxError overrides the maximum error when positive (clock.max_error).
	MaxError time.Duration
	// Logger receives a record whenever the synchronization state changes.
	// Nil disables logging.
	Logger *slog.Logger
}

// source reads the kernel clock state.
//
//declscope:package // the hook each platform file implements, and tests replace
type source interface {
	read() (synced bool, maxErr time.Duration, err error)
}

// Monitor tracks the local clock state. Its methods are safe for concurrent
// use.
type Monitor struct {
	cfg   Config
	src   source
	start sync.Once

	mu     sync.Mutex
	synced bool
	maxErr time.Duration
	known  bool // at least one poll has completed
	// failing is set while the kernel state cannot be read; poll alone
	// touches it.
	failing bool
}

// NewMonitor returns a Monitor reading the system clock. Until Start is called
// the kernel is not read: Status reports an unsynchronized clock with
// 16 s, subject to cfg.
func NewMonitor(cfg Config) *Monitor {
	return newMonitor(cfg, systemSource())
}

func newMonitor(cfg Config, src source) *Monitor {
	return &Monitor{cfg: cfg, src: src, maxErr: maxErrorLimit}
}

// Start reads the clock once, so that Status is current when Start returns,
// and then keeps reading it every second until ctx is done. When cfg
// overrides both the state and the maximum error, nothing is read. Calls
// after the first do nothing.
func (m *Monitor) Start(ctx context.Context) {
	m.start.Do(func() {
		if m.cfg.Sync != SyncAuto && m.cfg.MaxError > 0 {
			return
		}
		m.poll()
		go m.run(ctx)
	})
}

func (m *Monitor) run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.poll()
		}
	}
}

func (m *Monitor) poll() {
	synced, maxErr, err := m.src.read()
	if err != nil {
		synced, maxErr = false, maxErrorLimit
	}
	maxErr = min(max(maxErr, 0), maxErrorLimit)

	m.mu.Lock()
	changed := !m.known || synced != m.synced
	m.synced, m.maxErr, m.known = synced, maxErr, true
	m.mu.Unlock()

	// A read failure is logged when it starts and when it ends, whether or
	// not the reported state changes with it.
	failed, recovered := err != nil && !m.failing, err == nil && m.failing
	m.failing = err != nil
	l := m.cfg.Logger
	switch {
	case l == nil:
	case failed:
		l.Warn("cannot read the clock state; treating the clock as unsynchronized", "error", err)
	case recovered:
		l.Info("clock state readable again", "synchronized", synced, "max_error", maxErr)
	case changed && err == nil:
		l.Info("clock synchronization state", "synchronized", synced, "max_error", maxErr)
	}
}

// Status returns whether the clock is synchronized and its maximum error,
// with the configured overrides applied.
func (m *Monitor) Status() (synced bool, maxErr time.Duration) {
	m.mu.Lock()
	synced, maxErr = m.synced, m.maxErr
	m.mu.Unlock()

	switch m.cfg.Sync {
	case SyncSynced:
		synced = true
	case SyncUnsynced:
		synced = false
	}
	if m.cfg.MaxError > 0 {
		maxErr = m.cfg.MaxError
	}
	return synced, maxErr
}
