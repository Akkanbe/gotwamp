package cli

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// newReflectorCommand returns "gotwamp reflector status|sessions".
//
//declscope:package // a subcommand of the root
func newReflectorCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "reflector", Short: "Show the reflector role of gotwampd"}
	run := func(f func(context.Context, gotwampv1.ReflectorServiceClient, io.Writer) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			conn, err := g.conn()
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
			defer cancel()
			return f(ctx, gotwampv1.NewReflectorServiceClient(conn), cmd.OutOrStdout())
		}
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Listeners, connections, flows and packet counters",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c gotwampv1.ReflectorServiceClient, w io.Writer) error {
				resp, err := c.GetReflectorStatus(ctx, &gotwampv1.GetReflectorStatusRequest{})
				if err != nil {
					return g.rpcError(err)
				}
				if g.output != "table" {
					return g.printProto(resp)
				}
				return printReflectorStatus(w, resp)
			}),
		},
		&cobra.Command{
			Use:   "sessions",
			Short: "Full test sessions and Light flows",
			Args:  cobra.NoArgs,
			RunE: run(func(ctx context.Context, c gotwampv1.ReflectorServiceClient, w io.Writer) error {
				resp, err := c.ListReflectorSessions(ctx, &gotwampv1.ListReflectorSessionsRequest{})
				if err != nil {
					return g.rpcError(err)
				}
				if g.output != "table" {
					return g.printProto(resp)
				}
				return printReflectorSessions(w, resp)
			}),
		},
	)
	return cmd
}

func printReflectorStatus(w io.Writer, s *gotwampv1.GetReflectorStatusResponse) error {
	if !s.GetEnabled() {
		_, err := fmt.Fprintln(w, "the reflector role is disabled")
		return err
	}
	tw := newTable(w, "KIND", "LISTENER", "RECEIVED", "REFLECTED")
	for _, l := range s.GetListeners() {
		recv, refl := fmt.Sprint(l.GetPacketsReceived()), fmt.Sprint(l.GetPacketsReflected())
		if l.GetKind() == gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL {
			recv, refl = "-", "-" // counted per session below
		}
		tw.row(kindName(l.GetKind()), l.GetAddress(), recv, refl)
	}
	if err := tw.flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\ncontrol connections: %d open, %d accepted, %d rejected\nfull sessions: %d\nlight flows: %d (%d packets of new flows handled statelessly)\n\n",
		s.GetControlConnectionsActive(), s.GetControlConnectionsAccepted(), s.GetControlConnectionsRejected(),
		s.GetFullSessionsActive(), s.GetLightFlowsActive(), s.GetLightFlowsOverflow())
	if err != nil {
		return err
	}
	tw = newTable(w, "KIND", "RECEIVED", "REFLECTED", "DROPPED")
	for _, c := range s.GetCounters() {
		var drops []string
		for _, d := range c.GetDropped() {
			if d.GetCount() > 0 {
				drops = append(drops, fmt.Sprintf("%s %d", strings.ToLower(strings.TrimPrefix(d.GetReason().String(), "DROP_REASON_")), d.GetCount()))
			}
		}
		if len(drops) == 0 {
			drops = []string{"0"}
		}
		tw.row(kindName(c.GetKind()), fmt.Sprint(c.GetPacketsReceived()), fmt.Sprint(c.GetPacketsReflected()), strings.Join(drops, ", "))
	}
	return tw.flush()
}

func printReflectorSessions(w io.Writer, s *gotwampv1.ListReflectorSessionsResponse) error {
	tw := newTable(w, "SID", "CONTROL PEER", "SENDER", "REFLECTOR", "SECURITY", "STATE", "RECEIVED", "REFLECTED")
	for _, f := range s.GetFullSessions() {
		tw.row(hex.EncodeToString(f.GetSid()), endpoint(f.GetControlPeer()), endpoint(f.GetSender()), endpoint(f.GetReflector()),
			securityName(f.GetSecurity()), strings.ToLower(strings.TrimPrefix(f.GetState().String(), "FULL_SESSION_STATE_")),
			fmt.Sprint(f.GetPacketsReceived()), fmt.Sprint(f.GetPacketsReflected()))
	}
	if err := tw.flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	tw = newTable(w, "LIGHT FLOW SOURCE", "DESTINATION", "NEXT SEQ", "RECEIVED", "REFLECTED", "LAST RECEIVED")
	for _, f := range s.GetLightFlows() {
		tw.row(endpoint(f.GetSource()), endpoint(f.GetDestination()), fmt.Sprint(f.GetNextSeq()),
			fmt.Sprint(f.GetPacketsReceived()), fmt.Sprint(f.GetPacketsReflected()), f.GetLastReceivedAt().AsTime().Local().Format("15:04:05"))
	}
	return tw.flush()
}

// securityName names a security mode as the configuration does; "-" when
// unknown.
func securityName(m gotwampv1.SecurityMode) string {
	if m == gotwampv1.SecurityMode_SECURITY_MODE_UNSPECIFIED {
		return "-"
	}
	return strings.ToLower(strings.TrimPrefix(m.String(), "SECURITY_MODE_"))
}

func kindName(k gotwampv1.ReflectorKind) string {
	return strings.ToLower(strings.TrimPrefix(k.String(), "REFLECTOR_KIND_"))
}
