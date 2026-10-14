package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// newSessionCommand returns "gotwamp session" and its subcommands (SPEC
// §10.2).
//
//declscope:package // a subcommand of the root
func newSessionCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Manage the monitoring sessions of gotwampd"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List the monitoring sessions",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
					resp, err := c.ListSessions(ctx, &gotwampv1.ListSessionsRequest{})
					if err != nil {
						return g.rpcError(err)
					}
					if g.output != "table" {
						return g.printProto(resp)
					}
					return printSessions(cmd.OutOrStdout(), resp.GetSessions())
				})
			},
		},
		&cobra.Command{
			Use:     "show NAME",
			Aliases: []string{"get"},
			Short:   "Show a session's definition and latest result",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
					resp, err := c.GetSession(ctx, &gotwampv1.GetSessionRequest{Name: args[0]})
					if err != nil {
						return g.rpcError(err)
					}
					return g.printSession(cmd.OutOrStdout(), resp.GetSession())
				})
			},
		},
		newSessionEditCommand(g, "add", []string{"create"}),
		newSessionEditCommand(g, "update", nil),
		&cobra.Command{
			Use:     "remove NAME",
			Aliases: []string{"delete", "rm"},
			Short:   "Delete a session created through the API",
			Args:    cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
					if _, err := c.DeleteSession(ctx, &gotwampv1.DeleteSessionRequest{Name: args[0]}); err != nil {
						return g.rpcError(err)
					}
					if g.output == "table" {
						_, err := fmt.Fprintf(cmd.OutOrStdout(), "session %s removed\n", args[0])
						return err
					}
					return g.printProto(&gotwampv1.DeleteSessionResponse{})
				})
			},
		},
		newSessionStateCommand(g, "start", "Resume a stopped session"),
		newSessionStateCommand(g, "stop", "Pause a session, aborting its run"),
	)
	return cmd
}

// call runs f with a SenderService client and the RPC timeout.
//
//declscope:package // the result commands talk to the SenderService too
func (g *globals) call(cmd *cobra.Command, f func(context.Context, gotwampv1.SenderServiceClient) error) error {
	conn, err := g.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()
	return f(ctx, gotwampv1.NewSenderServiceClient(conn))
}

func newSessionStateCommand(g *globals, verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " NAME",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
				var s *gotwampv1.Session
				if verb == "start" {
					resp, err := c.StartSession(ctx, &gotwampv1.StartSessionRequest{Name: args[0]})
					if err != nil {
						return g.rpcError(err)
					}
					s = resp.GetSession()
				} else {
					resp, err := c.StopSession(ctx, &gotwampv1.StopSessionRequest{Name: args[0]})
					if err != nil {
						return g.rpcError(err)
					}
					s = resp.GetSession()
				}
				if g.output != "table" {
					return g.printProto(s)
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "session %s %s\n", args[0], stateName(s.GetState()))
				return err
			})
		},
	}
}

// newSessionEditCommand returns "session add" or "session update": the
// session comes from a YAML file (-f, the schema of sender.sessions[]) or
// from the ping flags plus the schedule flags.
func newSessionEditCommand(g *globals, verb string, aliases []string) *cobra.Command {
	var (
		file   string
		target string
		sf     scheduleFlags
	)
	pf := newTestFlags()
	cmd := &cobra.Command{
		Use:     verb + " [NAME] (-f FILE.yaml | --target TARGET [flags])",
		Aliases: aliases,
		Short:   map[string]string{"add": "Create a session through the API", "update": "Replace a session created through the API"}[verb],
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var spec *gotwampv1.SessionSpec
			var err error
			switch {
			case file != "":
				if spec, err = readSessionFile(file); err != nil {
					return exitf(ExitUsage, "%s: %w", file, err)
				}
				if len(args) == 1 {
					if spec.GetName() != "" && spec.GetName() != args[0] {
						return exitf(ExitUsage, "NAME %q differs from the name %q in %s", args[0], spec.GetName(), file)
					}
					spec.Name = args[0]
				}
			case target != "" && len(args) == 1:
				test, err := pf.spec(cmd.Flags(), target)
				if err != nil {
					return err
				}
				spec = &gotwampv1.SessionSpec{Name: args[0], Spec: test, Schedule: sf.schedule(), Labels: sf.labels}
			default:
				return exitf(ExitUsage, "give -f FILE.yaml, or NAME and --target")
			}
			return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
				var s *gotwampv1.Session
				if verb == "add" {
					resp, err := c.CreateSession(ctx, &gotwampv1.CreateSessionRequest{Spec: spec})
					if err != nil {
						return g.rpcError(err)
					}
					s = resp.GetSession()
				} else {
					resp, err := c.UpdateSession(ctx, &gotwampv1.UpdateSessionRequest{Spec: spec})
					if err != nil {
						return g.rpcError(err)
					}
					s = resp.GetSession()
				}
				return g.printSession(cmd.OutOrStdout(), s)
			})
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "YAML file with one session (the schema of sender.sessions[])")
	f.StringVar(&target, "target", "", "reflector host name or address")
	pf.register(f)
	sf.register(cmd)
	return cmd
}

// scheduleFlags are the schedule and labels of a session given as flags.
type scheduleFlags struct {
	continuous bool
	frequency  time.Duration
	jitter     time.Duration
	window     time.Duration
	labels     map[string]string
}

func (sf *scheduleFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&sf.continuous, "continuous", false, "run continuously, summarizing every --window")
	f.DurationVar(&sf.frequency, "frequency", 60*time.Second, "interval between periodic runs")
	f.DurationVar(&sf.jitter, "jitter", 0, "random delay before the first run")
	f.DurationVar(&sf.window, "window", 60*time.Second, "summary window of a continuous session")
	f.StringToStringVar(&sf.labels, "label", nil, "Prometheus label KEY=VALUE (repeatable)")
}

func (sf *scheduleFlags) schedule() *gotwampv1.Schedule {
	if sf.continuous {
		return &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS, Window: durationpb.New(sf.window)}
	}
	return &gotwampv1.Schedule{
		Type:      gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC,
		Frequency: durationpb.New(sf.frequency),
		Jitter:    durationpb.New(sf.jitter),
	}
}

func readSessionFile(path string) (*gotwampv1.SessionSpec, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	return parseSessionFile(b)
}

// printSession shows one session: its definition, state and latest result.
func (g *globals) printSession(w io.Writer, s *gotwampv1.Session) error {
	if g.output != "table" {
		return g.printProto(s)
	}
	spec := s.GetSpec()
	t := spec.GetSpec()
	var b strings.Builder
	row := func(k, format string, args ...any) { fmt.Fprintf(&b, "%-10s %s\n", k, fmt.Sprintf(format, args...)) }
	row("name", "%s", spec.GetName())
	row("target", "%s (%s)", t.GetTarget(), modeName(t.GetMode()))
	row("schedule", "%s", scheduleText(spec.GetSchedule()))
	row("test", "%d packets, interval %v, timeout %v, padding %d, dscp %d, ttl %d",
		t.GetCount(), t.GetInterval().AsDuration(), t.GetTimeout().AsDuration(), t.GetPadding(), t.GetDscp(), t.GetTtl())
	row("state", "%s (%s)%s", stateName(s.GetState()), sourceName(s.GetSource()), map[bool]string{true: ", run in progress"}[s.GetRunning()])
	row("runs", "%d ok, %d failed, %d skipped", s.GetRunsOk(), s.GetRunsFailed(), s.GetRunsSkipped())
	if labels := spec.GetLabels(); len(labels) > 0 {
		var kv []string
		for k, v := range labels {
			kv = append(kv, k+"="+v)
		}
		row("labels", "%s", strings.Join(sorted(kv), ", "))
	}
	if e := s.GetLastError(); e != nil {
		row("last error", "%s: %s", stageName(e.GetStage()), e.GetMessage())
	}
	if sum := s.GetLastSummary(); sum != nil {
		b.WriteString(summaryText(t.GetTarget(), sum))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func printSessions(w io.Writer, sessions []*gotwampv1.Session) error {
	tw := newTable(w, "NAME", "TARGET", "MODE", "SCHEDULE", "STATE", "SOURCE", "LAST RESULT")
	for _, s := range sessions {
		spec := s.GetSpec()
		tw.row(spec.GetName(), spec.GetSpec().GetTarget(), modeName(spec.GetSpec().GetMode()), scheduleText(spec.GetSchedule()),
			stateName(s.GetState()), sourceName(s.GetSource()), lastResult(s))
	}
	return tw.flush()
}

// lastResult is the one-line latest result of a session.
func lastResult(s *gotwampv1.Session) string {
	if e := s.GetLastError(); e != nil {
		return "error in " + stageName(e.GetStage()) + ": " + e.GetMessage()
	}
	if sum := s.GetLastSummary(); sum != nil {
		return summaryLine(sum)
	}
	return "-"
}

func scheduleText(s *gotwampv1.Schedule) string {
	if s.GetType() == gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS {
		return "continuous, window " + s.GetWindow().AsDuration().String()
	}
	return "every " + s.GetFrequency().AsDuration().String()
}

func stateName(s gotwampv1.SessionState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SESSION_STATE_"))
}

func sourceName(s gotwampv1.SessionSource) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SESSION_SOURCE_"))
}

func modeName(m gotwampv1.Mode) string {
	return strings.ToLower(strings.TrimPrefix(m.String(), "MODE_"))
}
