//declscope:ignore overexported // API for cmd/gotwamp, which shrink does not judge (package main)

// Package cli implements the gotwamp command (SPEC §10): a thin gRPC client
// of gotwampd over its Unix domain socket, or over TCP with mutual TLS
// (--server, ADR 0010).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/Akkanbe/gotwamp/internal/buildinfo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Exit statuses (SPEC §10.4).
const (
	ExitOK          = 0
	ExitFailure     = 1   // all packets lost, or a general error
	ExitUsage       = 2   // bad arguments or configuration
	ExitUnavailable = 3   // gotwampd cannot be reached
	ExitProtocol    = 4   // protocol or control error
	ExitInterrupted = 130 // SIGINT
)

// DefaultSocket is the gotwampd API socket used without --socket or
// GOTWAMP_SOCKET.
const DefaultSocket = "/run/gotwamp/gotwampd.sock"

// BuildInfo identifies the gotwamp binary.
type BuildInfo = buildinfo.Info

// Env is the outside world of the command.
type Env struct {
	Stdout, Stderr io.Writer
	// Getenv reads the environment; nil means os.Getenv.
	Getenv func(string) string
	// Dial connects to gotwampd at socket; nil dials the Unix socket.
	Dial func(socket string) (*grpc.ClientConn, error)
	// Interrupt delivers SIGINT and SIGTERM. The first one stops a running
	// ping gracefully (its summary is shown) and cancels other commands;
	// the second cancels everything. Nil means no signals.
	Interrupt <-chan struct{}
}

// exitError carries the exit status of a failed command.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

func (e *exitError) Unwrap() error { return e.err }

// exitf returns an error that makes gotwamp exit with code.
//
//declscope:package // every subcommand reports its failures through it
func exitf(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// globals are the global flags and the environment of one invocation.
//
//declscope:package // every subcommand reads them
type globals struct {
	info    BuildInfo
	output  string // table | json | jsonl
	timeout time.Duration
	// env is the outside world; subcommands use it through conn and the
	// cobra command's writers.
	//
	//declscope:private
	env Env
	//declscope:private
	socket string
	// remote replaces the socket with the API over TCP when set.
	//
	//declscope:private
	remote remoteFlags
	//declscope:private
	json bool
	//declscope:private
	noColor bool
	// softStop handles the first interrupt instead of a cancel.
	//
	//declscope:private
	softStop atomic.Pointer[func()]
}

// onInterrupt makes f handle the first interrupt; nil restores canceling.
//
//declscope:package // ping stops its run gracefully through it
func (g *globals) onInterrupt(f func()) {
	if f == nil {
		g.softStop.Store(nil)
		return
	}
	g.softStop.Store(&f)
}

// watchInterrupts calls the soft stop or cancel on each interrupt until ctx
// is done, and reports whether one arrived.
func (g *globals) watchInterrupts(ctx context.Context, cancel context.CancelFunc, interrupted *atomic.Bool) {
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-g.env.Interrupt:
			if !ok {
				return
			}
			interrupted.Store(true)
			if f := g.softStop.Load(); n == 1 && f != nil {
				(*f)()
			} else {
				cancel()
			}
		}
	}
}

// Main runs gotwamp with args and returns the exit status. ctx ends on
// SIGINT; a command it interrupts exits with ExitInterrupted.
func Main(ctx context.Context, args []string, info BuildInfo, env Env) int {
	if env.Getenv == nil {
		env.Getenv = os.Getenv
	}
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	g := &globals{env: env, info: info}
	root := newRootCommand(g)
	root.SetArgs(args)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var interrupted atomic.Bool
	if env.Interrupt != nil {
		go g.watchInterrupts(cmdCtx, cancel, &interrupted)
	}
	err := root.ExecuteContext(cmdCtx)
	if err == nil {
		return ExitOK
	}
	if ctx.Err() != nil || (interrupted.Load() && cmdCtx.Err() != nil) {
		return ExitInterrupted
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil && ee.err.Error() != "" {
			_, _ = fmt.Fprintln(env.Stderr, "gotwamp:", ee.err)
		}
		return ee.code
	}
	// Anything else comes from cobra: an unknown command, flag or argument.
	_, _ = fmt.Fprintln(env.Stderr, "gotwamp:", err)
	return ExitUsage
}

func newRootCommand(g *globals) *cobra.Command {
	socket := g.env.Getenv("GOTWAMP_SOCKET")
	if socket == "" {
		socket = DefaultSocket
	}
	root := &cobra.Command{
		Use:           "gotwamp",
		Short:         "Command line client of gotwampd, the TWAMP daemon",
		Version:       g.info.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := g.remote.check(cmd.Flags().Changed); err != nil {
				return err
			}
			if g.json {
				g.output = "json"
			}
			switch g.output {
			case "table", "json", "jsonl":
				return nil
			default:
				return exitf(ExitUsage, "--output %q is not table, json or jsonl", g.output)
			}
		},
	}
	root.SetVersionTemplate("gotwamp {{.Version}}\n")
	f := root.PersistentFlags()
	f.StringVar(&g.socket, "socket", socket, "gotwampd API socket (env GOTWAMP_SOCKET)")
	g.remote.register(f, g.env.Getenv)
	f.StringVar(&g.output, "output", "table", "output format: table, json or jsonl")
	f.BoolVar(&g.json, "json", false, "shorthand for --output json")
	f.DurationVar(&g.timeout, "timeout", 30*time.Second, "RPC timeout")
	// ping's own -L/--timeout (the loss timeout, SPEC §10.3) hides --timeout
	// there; --rpc-timeout reaches the same setting from every command.
	f.DurationVar(&g.timeout, "rpc-timeout", 30*time.Second, "RPC timeout (the same as --timeout, which ping uses for the loss timeout)")
	f.BoolVar(&g.noColor, "no-color", false, "disable colors (output is never colored yet)")
	root.AddCommand(newVersionCommand(g), newStatusCommand(g), newConfigCommand(g), newPingCommand(g),
		newSessionCommand(g), newStatsCommand(g), newWatchCommand(g), newReflectorCommand(g))
	return root
}

// conn connects to gotwampd. The connection is lazy: an unreachable daemon
// shows in the first RPC, which rpcError turns into ExitUnavailable.
//
//declscope:package // every subcommand that talks to gotwampd connects through it
func (g *globals) conn() (*grpc.ClientConn, error) {
	if g.remote.server != "" {
		return g.remote.dial()
	}
	if g.env.Dial != nil {
		return g.env.Dial(g.socket)
	}
	// "unix:" takes a relative path as well; after "unix://" the first
	// element of a relative path would be read as the URI authority.
	c, err := grpc.NewClient("unix:"+g.socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, exitf(ExitUnavailable, "cannot connect to gotwampd at %s: %w", g.socket, err)
	}
	return c, nil
}

// warn prints a warning on the standard error.
//
//declscope:package // ping warns of oversized packets
func (g *globals) warn(msg string) {
	_, _ = fmt.Fprintln(g.env.Stderr, "gotwamp: warning:", msg)
}

// rpcError maps a failed RPC to an exit status.
//
//declscope:package // every subcommand that talks to gotwampd reports through it
func (g *globals) rpcError(err error) error {
	st, _ := status.FromError(err)
	switch st.Code() {
	case codes.Unavailable:
		at := g.socket
		if g.remote.server != "" {
			at = g.remote.server
		}
		return exitf(ExitUnavailable, "cannot connect to gotwampd at %s: %s", at, st.Message())
	case codes.InvalidArgument:
		return exitf(ExitUsage, "%s", st.Message())
	case codes.Unimplemented:
		return exitf(ExitProtocol, "%s", st.Message())
	case codes.DeadlineExceeded:
		return exitf(ExitFailure, "gotwampd did not answer within --rpc-timeout %v: %s", g.timeout, st.Message())
	case codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition, codes.ResourceExhausted, codes.Aborted:
		// gotwampd's message says it all; the code's name means nothing to
		// an operator.
		return exitf(ExitFailure, "%s", st.Message())
	default:
		return exitf(ExitFailure, "%s: %s", st.Code(), st.Message())
	}
}

// printProto writes m as indented JSON (--output json) or one line
// (--output jsonl).
//
//declscope:package // the JSON outputs of every subcommand
func (g *globals) printProto(m proto.Message) error {
	opts := protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}
	if g.output == "json" {
		opts.Multiline, opts.Indent = true, "  "
	}
	b, err := opts.Marshal(m)
	if err != nil {
		return exitf(ExitFailure, "encode %T: %w", m, err)
	}
	_, err = fmt.Fprintln(g.env.Stdout, string(b))
	return err
}
