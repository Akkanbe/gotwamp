package schedule

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
	"github.com/Akkanbe/gotwamp/internal/ulid"
)

// deps are what a session needs from its Scheduler.
//
//declscope:package // the Scheduler hands them to every session
type deps struct {
	runner     Runner
	sink       store.ResultSink
	rand       io.Reader
	logger     *slog.Logger
	retryDelay time.Duration
}

// session is the scheduler goroutine of one monitoring session (SPEC §4.4).
//
//declscope:package // the Scheduler keeps one per session
type session struct {
	spec SessionSpec
	//declscope:private
	info *store.SessionInfo
	//declscope:private
	deps deps

	//declscope:private
	cancel context.CancelFunc
	//declscope:private
	done chan struct{}

	//declscope:private
	running atomic.Bool
	//declscope:private
	success, failed, skipped atomic.Uint64
	//declscope:private
	mu sync.Mutex
	//declscope:private
	lastRun time.Time
	//declscope:private
	lastErr string
}

//declscope:package // the Scheduler creates every session
func newSession(spec SessionSpec, d deps) *session {
	info := spec.Info
	info.Name = spec.Name
	return &session{spec: spec, info: &info, deps: d}
}

// start launches the session goroutine under parent.
//
//declscope:package // the Scheduler starts sessions
func (s *session) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		if s.spec.Schedule.Type == Continuous {
			s.continuous(ctx)
		} else {
			s.periodic(ctx)
		}
	}()
}

// interrupt cancels the session without waiting, so that several sessions
// can wind their runs down at once before stop waits for each.
//
//declscope:package // the Scheduler stops sessions
func (s *session) interrupt() {
	if s.cancel != nil {
		s.cancel()
	}
}

// stop cancels the session and waits for its run to end. It does nothing
// on a session not started.
//
//declscope:package // the Scheduler stops sessions
func (s *session) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel, s.done = nil, nil
}

//declscope:package // the Scheduler reports sessions
func (s *session) status(state State) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Spec:       s.spec,
		State:      state,
		Running:    s.running.Load(),
		RunsOK:     s.success.Load(),
		RunsFailed: s.failed.Load(),
		Skipped:    s.skipped.Load(),
		LastRun:    s.lastRun,
		LastError:  s.lastErr,
	}
}

// periodic starts a run every Frequency, after a random delay in
// [0, Jitter). A run still going at the next start skips it (SPEC §9.3).
func (s *session) periodic(ctx context.Context) {
	freq := s.spec.Schedule.Frequency
	next := time.Now().Add(s.jitter())
	var running chan struct{}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		timer.Reset(time.Until(next))
		select {
		case <-ctx.Done():
			if running != nil {
				<-running
			}
			return
		case <-timer.C:
		}
		if running != nil && !isClosed(running) {
			s.skip()
		} else {
			done := make(chan struct{})
			running = done
			go func() {
				defer close(done)
				s.run(ctx, false)
			}()
		}
		next = next.Add(freq)
		if now := time.Now(); !next.After(now) {
			// Woken late: resume on the schedule without a burst.
			next = next.Add((now.Sub(next)/freq + 1) * freq)
		}
	}
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// continuous keeps one run going, summarized every Window. A run that ends
// by itself failed; it is restarted after the retry delay.
func (s *session) continuous(ctx context.Context) {
	if !sleep(ctx, s.jitter()) {
		return
	}
	for {
		s.run(ctx, true)
		if !sleep(ctx, s.deps.retryDelay) {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// jitter returns a random delay in [0, Jitter).
func (s *session) jitter() time.Duration {
	j := s.spec.Schedule.Jitter
	if j <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := io.ReadFull(s.deps.rand, b[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(j)) //nolint:gosec // G115: the remainder is below j, a positive Duration
}

func (s *session) skip() {
	s.skipped.Add(1)
	s.deps.logger.Warn("run skipped: previous run still in progress", "session", s.spec.Name)
	s.deps.sink.Publish(store.Event{Kind: store.EventRunSkipped, Time: time.Now(), Session: s.info})
}

// run carries out one run and publishes its events. A panic in the runner
// fails the run, not the daemon (SPEC §12.2).
func (s *session) run(ctx context.Context, continuous bool) {
	s.running.Store(true)
	defer s.running.Store(false)
	runID := ulid.New(time.Now(), s.deps.rand)
	log := s.deps.logger.With("session", s.spec.Name, "run_id", runID, "target", s.info.Target)
	log.Info("run started")

	test := s.spec.Test
	if continuous {
		test.Count = 0
		test.Window = s.spec.Schedule.Window
	}
	spec := s.spec
	spec.Test = test
	// The summary of a periodic run waits for the runner to return, so that
	// a run that fails after its summary is counted once, as failed.
	var held *sender.Summary
	hooks := s.hooks(runID, continuous, log, &held)

	err := func() (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = &StageError{Stage: "internal", Err: fmt.Errorf("panic: %v", v)}
			}
		}()
		_, err = s.deps.runner.Run(ctx, &spec, hooks)
		return err
	}()
	// An error after cancellation is the cancellation itself.
	failed := err != nil && ctx.Err() == nil
	if held != nil {
		held.Invalid = held.Invalid || failed
		// One line ends a periodic run: its summary. A failure after the
		// summary follows as its own line.
		msg := "run ended"
		if failed {
			msg = "run summary"
		}
		s.summarize(runID, store.EventRunSummary, *held, log, msg)
	}
	if !failed {
		if held == nil {
			log.Info("run ended") // without a summary: stopped before one
		}
		return
	}
	re := &store.RunError{Stage: "test", Message: err.Error()}
	if se, ok := errors.AsType[*StageError](err); ok {
		re.Stage, re.Accept = se.Stage, se.Accept
	}
	now := time.Now()
	s.failed.Add(1)
	s.mu.Lock()
	s.lastRun, s.lastErr = now, err.Error()
	s.mu.Unlock()
	log.Warn("run failed", "stage", re.Stage, "error", err)
	s.deps.sink.Publish(store.Event{Kind: store.EventRunError, Time: now, Session: s.info, RunID: runID, Error: re})
}

// hooks turns the results of a run into events. The summary of a
// continuous window is published at once; that of a periodic run is left in
// held for run to publish.
func (s *session) hooks(runID string, continuous bool, log *slog.Logger, held **sender.Summary) sender.Hooks {
	return sender.Hooks{
		Packet: func(p sender.PacketResult) {
			s.deps.sink.Publish(store.Event{
				Kind: store.EventPacket, Time: p.Record.T4, Session: s.info, RunID: runID, Packet: p,
			})
		},
		Lost: func(l sender.LostPacket) {
			s.deps.sink.Publish(store.Event{
				Kind: store.EventLost, Time: l.T1, Session: s.info, RunID: runID, Lost: l,
			})
		},
		Summary: func(sum sender.Summary) {
			if continuous {
				s.summarize(runID, store.EventWindowSummary, sum, log, "window ended")
				return
			}
			*held = &sum
		},
	}
}

// summarize counts, logs as msg and publishes one summary. Only a summary
// neither aborted nor invalid counts as a completed run.
func (s *session) summarize(runID string, kind store.EventKind, sum sender.Summary, log *slog.Logger, msg string) {
	if !sum.Aborted && !sum.Invalid {
		s.success.Add(1)
		s.mu.Lock()
		s.lastRun, s.lastErr = sum.EndedAt, ""
		s.mu.Unlock()
	}
	e := store.Event{Kind: kind, Time: sum.EndedAt, Session: s.info, RunID: runID, Summary: &sum}
	if sum.SID != (wire.SID{}) {
		e.SID = sum.SID[:]
	}
	log.Info(msg, "window", sum.Window, "aborted", sum.Aborted, "invalid", sum.Invalid,
		"local", sum.Local, "remote", sum.Remote, "sid", hex.EncodeToString(e.SID),
		"sent", sum.Sent, "received", sum.Received, "lost", sum.Lost)
	s.deps.sink.Publish(e)
}
