//declscope:ignore overexported // API for cmd/gotwampd, which shrink does not judge (package main)

// Package daemon wires gotwampd together (SPEC §4.1, §4.2, §9): from the
// configuration it starts the reflector role (Light reflector, TWAMP-Control
// server and Full reflector), the clock monitor, the monitoring sessions with
// their result store and exporters, and the gRPC API on a Unix domain
// socket. It reloads on SIGHUP and shuts down gracefully when its context
// ends.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/clock"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/export/jsonl"
	"github.com/Akkanbe/gotwamp/internal/export/prom"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// ErrStartup marks failures to bind a socket or acquire a resource at
// startup; gotwampd exits with status 2 on them (SPEC §9.1).
var ErrStartup = errors.New("startup failed")

// ErrRuntime marks a part of the daemon that failed after startup (the API
// server, a reflector listener, the metrics endpoint); the daemon stops on
// it rather than running on without it.
var ErrRuntime = errors.New("failed while running")

// shutdownBudget bounds the whole shutdown, within the 5 s of NFR-05.
const shutdownBudget = 4500 * time.Millisecond

// Options are the inputs of the daemon besides its configuration.
type Options struct {
	// ConfigPath is the file the configuration came from, re-read by
	// ReloadConfig and SIGHUP. Empty means the built-in defaults.
	ConfigPath string
	// Version and Commit identify the build, for GetStatus and the metrics.
	Version, Commit string
	// Logger receives the daemon's logs; nil discards them.
	Logger *slog.Logger
	// LogLevel, when set, is the level of Logger; a reload sets it from
	// daemon.log.level. Nil leaves the level as it is, as when the command
	// line fixes it.
	LogLevel *slog.LevelVar
}

// Daemon is a running gotwampd.
type Daemon struct {
	log *slog.Logger

	clock *clock.Monitor
	refl  *reflectors // nil when the reflector role is off
	// tsMode is the timestamping mode of the daemon's sockets.
	tsMode tstamp.Mode
	sched  *schedule.Scheduler
	jsonl  *jsonl.Writer
	// results is the result store; with sqlite, shutdown writes it out.
	results *resultStore
	metrics *metricsServer
	grpc    *grpc.Server
	sock    net.Listener
	// remote serves the API over TCP with mutual TLS; nil when off.
	remote net.Listener
	rl     *reloader

	// runCtx ends when the daemon stops; ad-hoc runs and watches end with it.
	runCtx context.Context //nolint:containedctx // the daemon's lifetime
	stop   context.CancelFunc
	wg     sync.WaitGroup
	// abort stops the daemon after a failure, as the end of the context
	// given to Start does.
	abort context.CancelFunc
	errMu sync.Mutex
	errs  []error
}

// Run starts the daemon and serves until ctx is done, then shuts down.
// Startup failures wrap ErrStartup.
func Run(ctx context.Context, cfg *config.Config, opts Options) error {
	d, err := Start(ctx, cfg, opts)
	if err != nil {
		return err
	}
	return d.Wait()
}

// Start binds every socket and starts serving in the background. The daemon
// stops when ctx is done; Wait returns once it has.
func Start(ctx context.Context, cfg *config.Config, opts Options) (_ *Daemon, err error) {
	d := &Daemon{log: opts.Logger}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}
	started := time.Now()
	d.runCtx, d.stop = context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			d.release()
		}
	}()
	keys, err := loadKeys(cfg)
	if err != nil {
		return nil, err
	}
	live := &liveConfig{keys: keys.sender}
	live.p.Store(cfg)

	syncMode, err := clock.ParseSync(cfg.Daemon.Clock.Sync)
	if err != nil {
		return nil, err
	}
	d.clock = clock.NewMonitor(clock.Config{Sync: syncMode, MaxError: cfg.Daemon.Clock.MaxError, Logger: d.log})
	d.clock.Start(d.runCtx)

	open := &socketOpener{iface: cfg.Daemon.HardwareInterface, log: d.log.With("component", "tstamp")}
	if cfg.Reflector.Enabled {
		if d.refl, err = startReflectors(cfg, keys.reflector, open, d.clock.Status, d.log); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStartup, err)
		}
	}
	d.tsMode = timestampingInEffect(d.refl, cfg.Daemon.Timestamping, cfg.Daemon.HardwareInterface)

	// Results flow from the scheduler through the SID filler to the store
	// and the exporters.
	if d.results, err = openStore(cfg, d.log.With("component", "store")); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStartup, err)
	}
	sinks := store.Fanout{d.results}
	if j := cfg.Export.JSONL; j.Enabled {
		records, err := jsonl.ParseRecords(j.Records)
		if err != nil {
			return nil, err
		}
		if d.jsonl, err = jsonl.Open(jsonl.Config{
			Path: j.Path, Records: records, MaxSize: int64(j.Rotate.MaxSize), MaxFiles: j.Rotate.MaxFiles, Daily: j.Rotate.Daily,
			Logger: d.log.With("component", "jsonl"),
		}); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStartup, err)
		}
		sinks = append(sinks, d.jsonl)
	}
	var collector *prom.Collector
	if cfg.Daemon.Metrics.Listen != "" {
		collector = d.newCollector(cfg, opts)
		sinks = append(sinks, collector)
		if d.metrics, err = listenMetrics(cfg.Daemon.Metrics.Listen, collector); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStartup, err)
		}
	}
	sids := newSIDSink(sinks)
	var controlSetup func(*store.SessionInfo, time.Duration)
	// drop frees the results and the metrics of a deleted session, whether
	// the API or a reload deleted it.
	drop := func(name string) {
		sids.set(name, nil)
		d.results.Delete(name)
		if collector != nil {
			collector.Forget(name)
		}
	}
	if collector != nil {
		controlSetup = collector.ObserveControlSetup
	}
	run := newRunner(d.clock.Status, sids, controlSetup)
	run.open = open
	if cfg.Sender.Enabled {
		d.sched = schedule.New(schedule.Config{Runner: run, Sink: sids, Logger: d.log.With("component", "schedule")})
		specs, err := configSessions(cfg, keys.sender)
		if err != nil {
			return nil, err
		}
		if _, err := d.sched.Update(specs); err != nil {
			return nil, err
		}
	}
	d.rl = &reloader{path: opts.ConfigPath, cfg: live, sched: d.sched, drop: drop, level: opts.LogLevel, log: d.log}

	mode, err := cfg.Daemon.API.FileMode()
	if err != nil {
		return nil, err
	}
	if d.sock, err = listenUnix(cfg.Daemon.API.Socket, mode, cfg.Daemon.API.SocketGroup, d.log); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStartup, err)
	}
	// The same server answers on both listeners: the API over TCP differs
	// only in who may reach it (ADR 0010).
	listeners := []net.Listener{d.sock}
	if t := cfg.Daemon.API.TCP; t.Listen != "" {
		if d.remote, err = listenRemote(t, d.log.With("component", "api-tcp")); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStartup, err)
		}
		listeners = append(listeners, d.remote)
		d.log.Info("API listening over TCP with mutual TLS", "address", d.remote.Addr().String(), "allowed_names", t.AllowedNames)
	}
	d.grpc = grpc.NewServer()
	gotwampv1.RegisterDaemonServiceServer(d.grpc, newDaemonService(live, opts, started, d.clock, d.tsMode, d.sched, d.rl, d.results))
	gotwampv1.RegisterReflectorServiceServer(d.grpc, reflectorService(d.refl))
	gotwampv1.RegisterSenderServiceServer(d.grpc, newSenderService(d.runCtx, live, run, d.sched, d.results, drop, d.log))

	d.refl.serve(d.runCtx, &d.wg, d.fail)
	if d.metrics != nil {
		d.wg.Go(func() {
			if err := d.metrics.serve(); err != nil {
				d.fail(err)
			}
		})
	}
	for _, ln := range listeners {
		d.wg.Go(func() {
			// A shutdown right after Start may stop the server before Serve
			// begins; that is a normal stop.
			if err := d.grpc.Serve(ln); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				d.fail(fmt.Errorf("grpc on %v: %w", ln.Addr(), err))
			}
		})
	}
	// Subscribe before returning, so that a SIGHUP right after Start is a
	// reload and not the default action (exit).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	ctx, d.abort = context.WithCancel(ctx)
	d.wg.Go(func() { d.handleSignals(ctx, hup) })
	d.log.Info("gotwampd started", "version", opts.Version, "socket", cfg.Daemon.API.Socket,
		"sender", cfg.Sender.Enabled, "reflector", cfg.Reflector.Enabled, "sessions", len(cfg.Sender.Sessions),
		"timestamping", d.tsMode.String(), "config", opts.ConfigPath)
	return d, nil
}

// newCollector returns the Prometheus collector of the daemon.
func (d *Daemon) newCollector(cfg *config.Config, opts Options) *prom.Collector {
	pc := prom.Config{
		Buckets: cfg.Daemon.Metrics.RTTBuckets,
		Clock:   d.clock,
		Version: opts.Version,
		Commit:  opts.Commit,
	}
	pc.Timestamping = d.tsMode.String()
	if d.refl != nil {
		pc.Reflector = prom.ReflectorSourceFunc(d.refl.snapshot)
	}
	if w := d.jsonl; w != nil {
		pc.JSONLDropped = w.Dropped
	}
	pc.Store = d.results.stats()
	// /healthz is 200 while the API answers (SPEC §12.4).
	socket := cfg.Daemon.API.Socket
	pc.Health = func() error {
		c, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return fmt.Errorf("API socket: %w", err)
		}
		return c.Close()
	}
	return prom.New(pc)
}

// release frees what Start acquired before it failed.
func (d *Daemon) release() {
	d.stop()
	if d.sched != nil {
		d.sched.Close()
	}
	d.refl.close()
	if d.metrics != nil {
		_ = d.metrics.ln.Close()
	}
	if d.jsonl != nil {
		_ = d.jsonl.Close()
	}
	_ = d.results.close()
	if d.remote != nil {
		_ = d.remote.Close()
	}
	if d.sock != nil {
		_ = d.sock.Close()
	}
}

// handleSignals reloads on SIGHUP until ctx is done, then shuts the daemon
// down.
func (d *Daemon) handleSignals(ctx context.Context, hup chan os.Signal) {
	defer signal.Stop(hup)
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return
		case <-hup:
			_, _ = d.rl.reload() // reload logs what happened
		}
	}
}

// shutdown stops the sessions (their Full runs send Stop-Sessions), the
// RPCs (streams end with runCtx), the reflector, the exporters and the
// clock monitor, all within shutdownBudget: each step that overruns its
// share is given up so that the next ones still run.
func (d *Daemon) shutdown() {
	d.log.Info("gotwampd stopping")
	deadline := time.Now().Add(shutdownBudget)
	d.stop()
	if d.sched != nil && !within(deadline.Add(-2*time.Second), d.sched.Close) {
		d.log.Warn("monitoring sessions did not stop in time; leaving them")
	}
	if !within(deadline.Add(-time.Second), d.grpc.GracefulStop) {
		d.log.Warn("RPCs did not finish in time; closing them")
		d.grpc.Stop()
	}
	if d.metrics != nil {
		d.metrics.shutdown(time.Until(deadline.Add(-500 * time.Millisecond)))
	}
	if d.jsonl != nil {
		// The file gets what is left of the budget; a sink that does not
		// take the records (a stalled pipe, FIFO or network file system)
		// loses the rest instead of holding up the exit (Codex M9).
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := d.jsonl.Shutdown(ctx)
		cancel()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			d.log.Warn("JSON Lines output did not take every record in time; dropped the rest",
				"dropped", d.jsonl.Dropped())
		case err != nil:
			d.record(fmt.Errorf("jsonl: %w", err))
		}
	}
	// The scheduler has stopped: the database gets the last results queued,
	// within what is left of the budget.
	var serr error
	if !within(deadline, func() { serr = d.results.close() }) {
		d.log.Warn("result store did not close in time; the last results may be lost")
	} else if serr != nil {
		d.record(fmt.Errorf("store: %w", serr))
	}
}

// within runs f and reports whether it returned by deadline. An f that
// overruns keeps running in the background.
func within(deadline time.Time, f func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// fail records a failure of a running part and stops the daemon.
func (d *Daemon) fail(err error) {
	d.log.Error("gotwampd part failed; stopping", "error", err)
	d.record(err)
	if d.abort != nil {
		d.abort()
	}
}

// record keeps err for Wait.
func (d *Daemon) record(err error) {
	d.errMu.Lock()
	d.errs = append(d.errs, err)
	d.errMu.Unlock()
}

// Wait blocks until the daemon has stopped and returns the failures of its
// parts, which wrap ErrRuntime.
func (d *Daemon) Wait() error {
	d.wg.Wait()
	d.log.Info("gotwampd stopped")
	d.errMu.Lock()
	defer d.errMu.Unlock()
	if err := errors.Join(d.errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrRuntime, err)
	}
	return nil
}

// LightAddrs returns the bound addresses of the Light reflector, or nil when
// it is off.
func (d *Daemon) LightAddrs() []netip.AddrPort { return d.refl.lightAddrs() }

// ControlAddrs returns the bound addresses of the TWAMP-Control server, or
// nil when it is off.
func (d *Daemon) ControlAddrs() []netip.AddrPort { return d.refl.controlAddrs() }

// RemoteAddr returns the bound address of the API over TCP, or nil when it
// is off.
func (d *Daemon) RemoteAddr() net.Addr {
	if d.remote == nil {
		return nil
	}
	return d.remote.Addr()
}

// MetricsAddr returns the bound address of the metrics endpoint, or nil
// when it is off.
func (d *Daemon) MetricsAddr() net.Addr {
	if d.metrics == nil {
		return nil
	}
	return d.metrics.addr()
}
