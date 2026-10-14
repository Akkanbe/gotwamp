// Command gotwampd is the gotwamp daemon: a TWAMP (RFC 5357) sender and
// reflector serving a gRPC API on a Unix domain socket (SPEC §9).
//
// Exit statuses (SPEC §9.1): 0 normal, 1 configuration error, 2 socket or
// permission error at startup, 3 a part of the daemon failed while running.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Akkanbe/gotwamp/internal/buildinfo"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/daemon"
)

// Set at link time: -ldflags "-X main.version=... -X main.commit=...".
var (
	version = ""
	commit  = ""
)

const (
	exitConfig  = 1
	exitStartup = 2
	// exitRuntime: a part of the daemon failed after startup.
	exitRuntime = 3
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// exitError carries an exit status out of cobra.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

// run runs gotwampd until ctx is done and returns the exit status.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var (
		configPath string
		logLevel   string
		logFormat  string
	)
	info := buildinfo.Complete(buildinfo.Info{Version: version, Commit: commit}, debug.ReadBuildInfo)
	v, c := info.Known()
	cmd := &cobra.Command{
		Use:           "gotwampd",
		Short:         "TWAMP sender and reflector daemon",
		Version:       info.String(),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(configPath)
			missing := errors.Is(err, fs.ErrNotExist)
			switch {
			case missing:
				// No file runs on the defaults (SPEC §9.1), at the default
				// path or at one --config names, rather than exiting into a
				// restart loop under systemd (B10). Either way a reload reads
				// the file once it exists.
				cfg = config.Default()
			case err != nil:
				return &exitError{exitConfig, err}
			}
			if logLevel != "" {
				cfg.Daemon.Log.Level = logLevel
			}
			// --log-format applies to the logger only: the configuration
			// keeps the file's format, so that a reload does not take the
			// flag for a change that needs a restart.
			format := cfg.Daemon.Log.Format
			if logFormat != "" {
				format = logFormat
			}
			log, level, err := daemon.NewLogger(stderr, format, cfg.Daemon.Log.Level)
			if err != nil {
				return &exitError{exitConfig, err}
			}
			switch {
			case missing && !cmd.Flags().Changed("config"):
				log.Info("no configuration file; running on the defaults (reflector role only) until it exists and is reloaded", "path", configPath)
			case missing:
				log.Warn("configuration file not found; running on the defaults (reflector role only) until it exists and is reloaded", "path", configPath)
			}
			for _, w := range cfg.Warnings() {
				log.Warn("configuration warning", "problem", w.String())
			}
			opts := daemon.Options{ConfigPath: configPath, Version: v, Commit: c, Logger: log, LogLevel: level}
			if logLevel != "" {
				// --log-level outlasts reloads, which would set the file's level.
				opts.LogLevel = nil
			}
			err = daemon.Run(cmd.Context(), cfg, opts)
			switch {
			case errors.Is(err, daemon.ErrStartup):
				return &exitError{exitStartup, err}
			case errors.Is(err, daemon.ErrRuntime):
				return &exitError{exitRuntime, err}
			case err != nil:
				return &exitError{exitConfig, err}
			}
			return nil
		},
	}
	cmd.SetVersionTemplate("gotwampd {{.Version}}\n")
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	f := cmd.Flags()
	f.StringVar(&configPath, "config", config.DefaultPath, "configuration file")
	f.StringVar(&logLevel, "log-level", "", "log level: debug, info, warn or error (overrides daemon.log.level)")
	f.StringVar(&logFormat, "log-format", "", "log format: json or text (overrides daemon.log.format)")

	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "gotwampd:", err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitConfig // a flag or argument error
}
