package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// newStatusCommand returns "gotwamp status".
//
//declscope:package // a subcommand of the root
func newStatusCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the state of gotwampd",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := g.conn()
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
			defer cancel()
			st, err := gotwampv1.NewDaemonServiceClient(conn).GetStatus(ctx, &gotwampv1.GetStatusRequest{})
			if err != nil {
				return g.rpcError(err)
			}
			if g.output != "table" {
				return g.printProto(st)
			}
			return printStatus(cmd.OutOrStdout(), st)
		},
	}
}

func printStatus(w io.Writer, st *gotwampv1.GetStatusResponse) error {
	var roles []string
	if st.GetSenderEnabled() {
		roles = append(roles, "sender")
	}
	if st.GetReflectorEnabled() {
		roles = append(roles, "reflector")
	}
	clk := st.GetClock()
	sync := "not synchronized"
	if clk.GetSynchronized() {
		sync = "synchronized"
	}
	source := "kernel"
	if clk.GetSource() == gotwampv1.ClockSyncSource_CLOCK_SYNC_SOURCE_CONFIG {
		source = "configured"
	}
	ts := strings.ToLower(strings.TrimPrefix(st.GetTimestampingMode().String(), "TIMESTAMPING_MODE_"))
	if ts == "unspecified" {
		ts = "auto (no socket open)"
	}
	cfg := st.GetConfigPath()
	if cfg == "" {
		cfg = "(built-in defaults)"
	}
	rows := [][2]string{
		{"gotwampd", fmt.Sprintf("%s (commit %s, %s)", orDash(st.GetVersion()), orDash(st.GetCommit()), st.GetGoVersion())},
		{"uptime", st.GetUptime().AsDuration().Round(time.Second).String()},
		{"roles", orDash(strings.Join(roles, ", "))},
		{"timestamping", ts},
		{"clock", fmt.Sprintf("%s, max error %v (%s)", sync, clk.GetMaxError().AsDuration(), source)},
		{"sessions", fmt.Sprint(st.GetSessionCount())},
		{"store", storeText(st.GetStore())},
		{"config", cfg},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "%-13s %s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	return nil
}

// storeText describes the result store: its backend and, for a database,
// its file and counters.
func storeText(s *gotwampv1.StoreStatus) string {
	switch {
	case s.GetBackend() == "":
		return "-"
	case s.GetPath() == "":
		return s.GetBackend()
	}
	return fmt.Sprintf("%s %s (%d results written, %d dropped, %d failed)",
		s.GetBackend(), s.GetPath(), s.GetResultsWritten(), s.GetResultsDropped(), s.GetResultsFailed())
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
