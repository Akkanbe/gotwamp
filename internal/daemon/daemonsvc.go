package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/clock"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// liveConfig is the configuration in effect, which a reload replaces.
//
//declscope:package // every service reads the configuration through it
type liveConfig struct {
	p atomic.Pointer[config.Config]
	// keys are sender.keys_file, read at startup; a reload keeps them.
	keys keystore.Store
}

// get returns the configuration in effect.
//
//declscope:package // every service reads the configuration through it
func (c *liveConfig) get() *config.Config { return c.p.Load() }

// reloader re-reads the configuration file and applies what can change
// without a restart (SPEC §9.3): the monitoring sessions, sender.defaults
// and the log level.
//
//declscope:package // Start builds it; SIGHUP and ReloadConfig use it
type reloader struct {
	path  string
	cfg   *liveConfig
	sched *schedule.Scheduler // nil when the sender role is off
	// drop frees the results and the metrics of a removed session.
	drop func(name string)
	// level is the daemon's log level, which a reload sets from
	// daemon.log.level; nil when it is fixed (gotwampd --log-level).
	level *slog.LevelVar
	log   *slog.Logger
	//declscope:private
	mu sync.Mutex
}

// errNoConfigFile is returned by a reload of a daemon running on defaults.
var errNoConfigFile = errors.New("gotwampd runs on the built-in defaults; there is no file to reload")

// reload applies the configuration file. On error nothing changes, and the
// failure is logged whether SIGHUP or ReloadConfig asked for the reload.
//
//declscope:package // SIGHUP reloads through it
func (r *reloader) reload() (*gotwampv1.ReloadConfigResponse, error) {
	resp, err := r.apply()
	if err != nil {
		r.log.Error("reload failed; keeping the running configuration", "config", r.path, "error", err)
	}
	return resp, err
}

// apply is reload without the logging of failures.
func (r *reloader) apply() (*gotwampv1.ReloadConfigResponse, error) {
	if r.path == "" {
		return nil, errNoConfigFile
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := config.Load(r.path)
	if err != nil {
		return nil, err
	}
	cur := r.cfg.get()
	diff := config.Diff(cur, next)

	// Only what applies at once is taken over; the rest waits for a restart.
	eff := *cur
	eff.Sender.Defaults = next.Sender.Defaults
	eff.Sender.Sessions = next.Sender.Sessions
	if r.level != nil {
		eff.Daemon.Log.Level = next.Daemon.Log.Level
	}

	resp := &gotwampv1.ReloadConfigResponse{
		SessionsAdded:     diff.SessionsAdded,
		SessionsRemoved:   diff.SessionsRemoved,
		SessionsUpdated:   diff.SessionsChanged,
		SessionsUnchanged: count32(len(diff.SessionsUnchanged)),
	}
	if r.sched != nil {
		specs, err := configSessions(&eff, r.cfg.keys)
		if err != nil {
			return nil, err
		}
		ch, err := r.sched.Update(specs)
		if err != nil {
			return nil, err
		}
		// Update has stopped the removed sessions: nothing publishes for them
		// any more.
		for _, name := range ch.Removed {
			r.drop(name)
		}
		resp.SessionsAdded, resp.SessionsRemoved, resp.SessionsUpdated = ch.Added, ch.Removed, ch.Changed
		resp.SessionsUnchanged = count32(len(ch.Unchanged))
	} else if len(next.Sender.Sessions) > 0 {
		resp.Warnings = append(resp.Warnings, "the sender role is off; sessions start after it is enabled and gotwampd restarts")
	}
	switch {
	case r.level != nil:
		var lv slog.Level
		if err := lv.UnmarshalText([]byte(next.Daemon.Log.Level)); err == nil {
			r.level.Set(lv)
		}
	case next.Daemon.Log.Level != cur.Daemon.Log.Level:
		resp.Warnings = append(resp.Warnings, "daemon.log.level is not applied: the log level is fixed by gotwampd --log-level")
	}
	r.cfg.p.Store(&eff)

	for _, w := range next.Warnings() {
		resp.Warnings = append(resp.Warnings, w.String())
	}
	if len(diff.Restart) > 0 {
		resp.Warnings = append(resp.Warnings, "restart gotwampd to apply changes of "+strings.Join(diff.Restart, ", "))
	}
	// A SIGHUP has no one to answer: the warnings go to the log too.
	for _, w := range resp.Warnings {
		r.log.Warn("reload: "+w, "config", r.path)
	}
	r.log.Info("configuration reloaded", "config", r.path,
		"added", resp.SessionsAdded, "removed", resp.SessionsRemoved, "updated", resp.SessionsUpdated,
		"unchanged", resp.SessionsUnchanged, "applied", diff.Applied, "restart_needed", diff.Restart)
	return resp, nil
}

// daemonService implements gotwamp.v1.DaemonService.
type daemonService struct {
	gotwampv1.UnimplementedDaemonServiceServer
	cfg      *liveConfig
	opts     Options
	started  time.Time
	clock    *clock.Monitor
	tsMode   tstamp.Mode
	sched    *schedule.Scheduler
	reloader *reloader
	results  *resultStore
}

// newDaemonService returns the DaemonService of a daemon started at started.
//
//declscope:package // Start registers it
func newDaemonService(cfg *liveConfig, opts Options, started time.Time, clk *clock.Monitor, ts tstamp.Mode, sched *schedule.Scheduler, rl *reloader, results *resultStore) *daemonService {
	return &daemonService{cfg: cfg, opts: opts, started: started, clock: clk, tsMode: ts, sched: sched, reloader: rl, results: results}
}

func (s *daemonService) GetStatus(context.Context, *gotwampv1.GetStatusRequest) (*gotwampv1.GetStatusResponse, error) {
	cfg := s.cfg.get()
	synced, maxErr := s.clock.Status()
	source := gotwampv1.ClockSyncSource_CLOCK_SYNC_SOURCE_KERNEL
	if cfg.Daemon.Clock.Sync != "auto" {
		source = gotwampv1.ClockSyncSource_CLOCK_SYNC_SOURCE_CONFIG
	}
	var sessions int
	if s.sched != nil {
		sessions = len(s.sched.List())
	}
	return &gotwampv1.GetStatusResponse{
		Version:          s.opts.Version,
		Commit:           s.opts.Commit,
		GoVersion:        runtime.Version(),
		StartedAt:        timestamppb.New(s.started),
		Uptime:           durationpb.New(time.Since(s.started)),
		SenderEnabled:    cfg.Sender.Enabled,
		ReflectorEnabled: cfg.Reflector.Enabled,
		TimestampingMode: protoTimestamping(s.tsMode),
		Clock: &gotwampv1.ClockStatus{
			Synchronized: synced,
			MaxError:     durationpb.New(maxErr),
			Source:       source,
		},
		SessionCount: count32(sessions),
		ConfigPath:   s.opts.ConfigPath,
		Store:        s.results.status(),
	}, nil
}

func (s *daemonService) ReloadConfig(context.Context, *gotwampv1.ReloadConfigRequest) (*gotwampv1.ReloadConfigResponse, error) {
	resp, err := s.reloader.reload()
	switch {
	case errors.Is(err, errNoConfigFile):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case err != nil:
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("reload failed, nothing changed: %v", err))
	}
	return resp, nil
}
