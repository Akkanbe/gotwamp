// Package schedule runs the monitoring sessions of gotwampd (SPEC §9.3):
// one scheduler goroutine per session starts periodic runs, or keeps one
// continuous run going, and publishes the results to a store.ResultSink.
//
// How a run is carried out (TWAMP Light with the sender alone, or TWAMP
// Full through TWAMP-Control first) is the Runner's business.
package schedule

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// defaultRetryDelay is how long a continuous session waits before
// restarting a run that failed.
const defaultRetryDelay = 5 * time.Second

// Type is the kind of schedule.
type Type uint8

const (
	// Periodic starts a run of Count packets every Frequency.
	Periodic Type = iota + 1
	// Continuous sends without a break and summarizes every Window.
	Continuous
)

// Schedule says when a session runs.
type Schedule struct {
	// Type selects periodic or continuous.
	Type Type
	// Frequency is the interval between periodic run starts.
	Frequency time.Duration
	// Jitter bounds the random delay before the first run, which spreads
	// the sessions started together.
	Jitter time.Duration
	// Window is the summary period of a continuous session.
	Window time.Duration
}

// Source tells where a session was defined.
type Source uint8

// The values of Source.
//
//declscope:ignore overexported // an enum: SourceConfig is the zero value the daemon's config sessions leave
const (
	// SourceConfig is a session of the configuration file. Update manages
	// these.
	SourceConfig Source = iota
	// SourceAPI is a session created through the API. It lasts until
	// removed or until the daemon restarts.
	SourceAPI
)

// SessionSpec defines a monitoring session.
type SessionSpec struct {
	// Name identifies the session.
	Name string
	// Source tells where the session was defined.
	Source Source
	// Schedule says when it runs.
	Schedule Schedule
	// Info describes the session in its results. Info.Name is set to Name.
	Info store.SessionInfo
	// Test holds the test parameters. For a continuous session Count is
	// replaced by 0 and Window by Schedule.Window.
	Test sender.Config
	// Options holds what the Runner needs besides Test, such as the
	// TWAMP-Control parameters of a Full session.
	Options any
}

// Runner carries out one run of spec, reporting its results through hooks.
// It returns when the run completes, fails or ctx is canceled.
type Runner interface {
	Run(ctx context.Context, spec *SessionSpec, hooks sender.Hooks) (sender.Summary, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, spec *SessionSpec, hooks sender.Hooks) (sender.Summary, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spec *SessionSpec, hooks sender.Hooks) (sender.Summary, error) {
	return f(ctx, spec, hooks)
}

// LightRunner runs TWAMP Light sessions with the sender alone. clock may be
// nil; see sender.WithClock.
func LightRunner(clock func() (bool, time.Duration)) Runner {
	return RunnerFunc(func(ctx context.Context, spec *SessionSpec, hooks sender.Hooks) (sender.Summary, error) {
		opts := []sender.Option{sender.WithHooks(hooks)}
		if clock != nil {
			opts = append(opts, sender.WithClock(clock))
		}
		s, err := sender.New(spec.Test, opts...)
		if err != nil {
			return sender.Summary{}, &StageError{Stage: "connect", Err: err}
		}
		defer s.Close()
		return s.Run(ctx)
	})
}

// StageError is a run failure with the stage it happened in (connect,
// greeting, setup, request, start, test or stop). Errors of other types
// are reported as stage "test".
type StageError struct {
	// Stage is where the run failed.
	Stage string
	// Accept is the Accept value the server answered, or 0.
	Accept int
	// Err is the cause.
	Err error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }

// Unwrap returns the cause.
func (e *StageError) Unwrap() error { return e.Err }

// State is the lifecycle state of a session.
type State uint8

// The values of State.
//
//declscope:ignore overexported // an enum: StateRunning is the zero value of a new session
const (
	// StateRunning sessions start runs on their schedule.
	StateRunning State = iota
	// StateStopped sessions are kept but start no runs.
	StateStopped
)

// String returns "running" or "stopped".
func (s State) String() string {
	if s == StateStopped {
		return "stopped"
	}
	return "running"
}

// Status is a snapshot of one session.
type Status struct {
	// Spec is the definition.
	Spec SessionSpec
	// State is the lifecycle state.
	State State
	// Running reports a run in progress.
	Running bool
	// RunsOK counts completed runs and windows.
	RunsOK uint64
	// RunsFailed counts failed runs.
	RunsFailed uint64
	// Skipped counts periodic runs skipped because the previous one was
	// still in progress.
	Skipped uint64
	// LastRun is when the last run or window completed or failed.
	LastRun time.Time
	// LastError is the error of the last run if it failed.
	LastError string
}

// Changes lists what Update did, by session name.
type Changes struct {
	Added, Removed, Changed, Unchanged []string
}

// Config parameterizes a Scheduler.
type Config struct {
	// Runner carries out the runs. It is required.
	Runner Runner
	// Sink receives every event. Nil discards them.
	Sink store.ResultSink
	// rand supplies the start jitter and run IDs. Nil means crypto/rand.
	rand io.Reader
	// Logger receives run starts, ends and failures. Nil means
	// slog.Default.
	Logger *slog.Logger
	// retryDelay is the pause before a failed continuous run restarts.
	// Zero means 5 s. Tests set it.
	retryDelay time.Duration
}

// Scheduler runs the monitoring sessions. Its methods are safe for
// concurrent use.
type Scheduler struct {
	deps   deps
	ctx    context.Context //nolint:containedctx // the parent of every session, canceled by Close
	cancel context.CancelFunc

	// op serializes the operations that start or stop sessions, which wait
	// for runs to end; mu guards the map and is never held while waiting.
	op       sync.Mutex
	mu       sync.Mutex
	sessions map[string]*entry
	closed   bool
}

type entry struct {
	s     *session
	state State
}

type discard struct{}

func (discard) Publish(store.Event) {}

// New returns a Scheduler with no sessions.
func New(cfg Config) *Scheduler {
	d := deps{runner: cfg.Runner, sink: cfg.Sink, rand: cfg.rand, logger: cfg.Logger, retryDelay: cfg.retryDelay}
	if d.sink == nil {
		d.sink = discard{}
	}
	if d.rand == nil {
		d.rand = rand.Reader
	}
	if d.logger == nil {
		d.logger = slog.Default()
	}
	if d.retryDelay <= 0 {
		d.retryDelay = defaultRetryDelay
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{deps: d, ctx: ctx, cancel: cancel, sessions: make(map[string]*entry)}
}

// ErrClosed is returned by operations on a closed Scheduler.
var ErrClosed = errors.New("schedule: scheduler closed")

// ErrNotFound is returned for an unknown session name.
var ErrNotFound = errors.New("schedule: no such session")

func validate(spec *SessionSpec) error {
	if spec.Name == "" {
		return errors.New("schedule: session name is empty")
	}
	switch sc := spec.Schedule; {
	case sc.Type == Periodic && sc.Frequency <= 0:
		return fmt.Errorf("schedule: session %q: frequency must be positive", spec.Name)
	case sc.Type == Continuous && sc.Window <= 0:
		return fmt.Errorf("schedule: session %q: window must be positive", spec.Name)
	case sc.Type != Periodic && sc.Type != Continuous:
		return fmt.Errorf("schedule: session %q: unknown schedule type %d", spec.Name, sc.Type)
	case sc.Jitter < 0:
		return fmt.Errorf("schedule: session %q: negative jitter", spec.Name)
	}
	return nil
}

// Update makes the configuration-defined sessions match specs (SPEC §9.3
// reload): sessions no longer listed stop and go, changed ones are stopped
// and recreated in their previous state, new ones start running, and
// unchanged ones are not touched. API-defined sessions are left alone; a
// name that collides with one is an error, and nothing changes.
func (s *Scheduler) Update(specs []SessionSpec) (Changes, error) {
	want := make(map[string]SessionSpec, len(specs))
	for _, spec := range specs {
		spec.Source = SourceConfig
		if err := validate(&spec); err != nil {
			return Changes{}, err
		}
		if _, dup := want[spec.Name]; dup {
			return Changes{}, fmt.Errorf("schedule: duplicate session %q", spec.Name)
		}
		want[spec.Name] = spec
	}

	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Changes{}, ErrClosed
	}
	// Check every name before changing anything, so that a failed Update
	// leaves the sessions as they were.
	for name := range want {
		if e, ok := s.sessions[name]; ok && e.s.spec.Source != SourceConfig {
			s.mu.Unlock()
			return Changes{}, fmt.Errorf("schedule: session %q is defined through the API", name)
		}
	}
	var ch Changes
	var stop []*entry
	start := map[string]State{}
	for name, e := range s.sessions {
		if e.s.spec.Source != SourceConfig {
			continue
		}
		spec, ok := want[name]
		switch {
		case !ok:
			ch.Removed = append(ch.Removed, name)
			stop = append(stop, e)
			delete(s.sessions, name)
		case reflect.DeepEqual(spec, e.s.spec):
			ch.Unchanged = append(ch.Unchanged, name)
		default:
			ch.Changed = append(ch.Changed, name)
			stop = append(stop, e)
			start[name] = e.state
		}
	}
	for name := range want {
		if _, ok := s.sessions[name]; !ok {
			ch.Added = append(ch.Added, name)
			start[name] = StateRunning
		}
	}
	s.mu.Unlock()

	// The runs wind down together: a Full run sends Stop-Sessions on the
	// way out, and a reload of many sessions should not wait for each in
	// turn.
	for _, e := range stop {
		e.s.interrupt()
	}
	for _, e := range stop {
		e.s.stop()
	}
	s.mu.Lock()
	for name, state := range start {
		s.sessions[name] = s.launch(want[name], state)
	}
	s.mu.Unlock()
	for _, l := range [][]string{ch.Added, ch.Removed, ch.Changed, ch.Unchanged} {
		slices.Sort(l)
	}
	return ch, nil
}

// launch creates a session and starts it unless state is StateStopped.
func (s *Scheduler) launch(spec SessionSpec, state State) *entry {
	e := &entry{s: newSession(spec, s.deps), state: state}
	if state == StateRunning {
		e.s.start(s.ctx)
	}
	return e
}

// Add creates and starts a session defined through the API.
func (s *Scheduler) Add(spec SessionSpec) error {
	spec.Source = SourceAPI
	if err := validate(&spec); err != nil {
		return err
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, ok := s.sessions[spec.Name]; ok {
		return fmt.Errorf("schedule: session %q exists", spec.Name)
	}
	s.sessions[spec.Name] = s.launch(spec, StateRunning)
	return nil
}

// Replace gives an existing session the definition spec, which then comes
// from the API, keeping its state: running or stopped. It is one operation:
// no other change comes between the end of the old definition's run and the
// start of the new one, and an invalid spec leaves the session as it was.
func (s *Scheduler) Replace(spec SessionSpec) error {
	spec.Source = SourceAPI
	if err := validate(&spec); err != nil {
		return err
	}
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	old, ok := s.sessions[spec.Name]
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	old.s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Close waits for op: the scheduler is still open.
	s.sessions[spec.Name] = s.launch(spec, old.state)
	return nil
}

// Remove stops a session and forgets it, whatever its source.
func (s *Scheduler) Remove(name string) error {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	e, ok := s.sessions[name]
	delete(s.sessions, name)
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	e.s.stop()
	return nil
}

// Start resumes a stopped session. It does nothing to a running one.
func (s *Scheduler) Start(name string) error {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	e, ok := s.sessions[name]
	if !ok {
		return ErrNotFound
	}
	if e.state == StateStopped {
		e.state = StateRunning
		e.s.start(s.ctx)
	}
	return nil
}

// Stop stops a session, aborting its run, and keeps it for Start.
func (s *Scheduler) Stop(name string) error {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	e, ok := s.sessions[name]
	if ok {
		e.state = StateStopped
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	e.s.stop()
	return nil
}

// Get returns the status of one session.
func (s *Scheduler) Get(name string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[name]
	if !ok {
		return Status{}, ErrNotFound
	}
	return e.s.status(e.state), nil
}

// List returns the status of every session, sorted by name.
func (s *Scheduler) List() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.sessions))
	for _, e := range s.sessions {
		out = append(out, e.s.status(e.state))
	}
	slices.SortFunc(out, func(a, b Status) int {
		switch {
		case a.Spec.Name < b.Spec.Name:
			return -1
		case a.Spec.Name > b.Spec.Name:
			return 1
		}
		return 0
	})
	return out
}

// Close stops every session and waits for their runs to end. Later
// operations fail with ErrClosed.
func (s *Scheduler) Close() {
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	s.closed = true
	all := make([]*entry, 0, len(s.sessions))
	for _, e := range s.sessions {
		all = append(all, e)
	}
	s.sessions = map[string]*entry{}
	s.mu.Unlock()
	s.cancel()
	for _, e := range all {
		e.s.stop()
	}
}
