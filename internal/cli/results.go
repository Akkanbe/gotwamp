package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// newStatsCommand returns "gotwamp stats NAME".
//
//declscope:package // a subcommand of the root
func newStatsCommand(g *globals) *cobra.Command {
	var (
		last  uint32
		since time.Duration
	)
	cmd := &cobra.Command{
		Use:   "stats NAME",
		Short: "Show the latest run or window summaries of a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &gotwampv1.GetSessionStatsRequest{Name: args[0], Last: last}
			if since > 0 {
				req.Since = timestamppb.New(time.Now().Add(-since))
				// A time range shows all of it unless --last says otherwise.
				if !cmd.Flags().Changed("last") {
					req.Last = 0
				}
			}
			return g.call(cmd, func(ctx context.Context, c gotwampv1.SenderServiceClient) error {
				resp, err := c.GetSessionStats(ctx, req)
				if err != nil {
					return g.rpcError(err)
				}
				if g.output != "table" {
					return g.printProto(resp)
				}
				tw := newTable(cmd.OutOrStdout(), "ENDED", "SENT", "LOST", "LOSS", "RTT MIN/MEDIAN/MAX (ms)", "IPDV (ms)", "NOTE")
				for _, s := range resp.GetSummaries() {
					note := ""
					if s.GetAborted() {
						note = "aborted"
					}
					rtt := s.GetRtt()
					tw.row(s.GetEndedAt().AsTime().Local().Format(time.DateTime), fmt.Sprint(s.GetSent()), fmt.Sprint(s.GetLost()),
						fmt.Sprintf("%.3f%%", 100*s.GetLossRatio()),
						fmt.Sprintf("%s/%s/%s", msN(rtt.GetMin()), msN(rtt.GetMedian()), msN(rtt.GetMax())),
						msN(s.GetJitter().GetIpdvRttMean()), note)
				}
				return tw.flush()
			})
		},
	}
	cmd.Flags().Uint32Var(&last, "last", 10, "number of summaries, the latest (0: all; with --since, all in the range unless given)")
	cmd.Flags().DurationVar(&since, "since", 0,
		"summaries of the last DURATION: from the database with store.backend sqlite (as far back as its retention), else from the history in memory")
	return cmd
}

// newWatchCommand returns "gotwamp watch [NAME ...]".
//
//declscope:package // a subcommand of the root
func newWatchCommand(g *globals) *cobra.Command {
	var packets bool
	cmd := &cobra.Command{
		Use:   "watch [NAME ...]",
		Short: "Stream the results of sessions as they come (all sessions by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := g.conn()
			if err != nil {
				return err
			}
			defer conn.Close()
			stream, err := gotwampv1.NewSenderServiceClient(conn).WatchResults(cmd.Context(), &gotwampv1.WatchResultsRequest{Sessions: args, Packets: packets})
			if err != nil {
				return g.rpcError(err)
			}
			w := cmd.OutOrStdout()
			for {
				ev, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					if cmd.Context().Err() != nil {
						return cmd.Context().Err()
					}
					return g.rpcError(err)
				}
				if g.output != "table" {
					if err := printJSONLine(w, ev); err != nil {
						return err
					}
					continue
				}
				if _, err := fmt.Fprintln(w, watchLine(ev)); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().BoolVar(&packets, "packets", false, "also stream per-packet results")
	return cmd
}

// watchLine renders one event of a watch.
func watchLine(ev *gotwampv1.TestEvent) string {
	head := fmt.Sprintf("%s %-16s", time.Now().Format("15:04:05"), ev.GetSession())
	switch e := ev.GetEvent().(type) {
	case *gotwampv1.TestEvent_Summary:
		kind := "run"
		if e.Summary.GetKind() == gotwampv1.SummaryKind_SUMMARY_KIND_WINDOW {
			kind = "window"
		}
		return head + " " + kind + " " + summaryLine(e.Summary)
	case *gotwampv1.TestEvent_Error:
		return head + " error in " + stageName(e.Error.GetStage()) + ": " + e.Error.GetMessage()
	case *gotwampv1.TestEvent_Packet:
		return head + " " + packetLine(e.Packet)
	case *gotwampv1.TestEvent_Lost:
		return fmt.Sprintf("%s seq=%-3d *LOST*", head, e.Lost.GetSeq())
	case *gotwampv1.TestEvent_Dropped:
		return fmt.Sprintf("%s (%d events dropped: the watch was too slow)", head, e.Dropped.GetCount())
	}
	return head
}
