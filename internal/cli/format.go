package cli

// The renderings shared by the commands: per-packet lines, summaries,
// tables and JSON lines.

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

//declscope:package // ping and watch print JSON lines with it
var jsonl = protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}

// printJSONLine writes m as one line of JSON.
//
//declscope:package // ping and watch print JSON lines with it
func printJSONLine(w io.Writer, m proto.Message) error {
	b, err := jsonl.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// endpoint formats an address and port.
//
//declscope:package // ping and reflector print endpoints
func endpoint(e *gotwampv1.Endpoint) string {
	a := e.GetAddress()
	if strings.Contains(a, ":") {
		a = "[" + a + "]"
	}
	return fmt.Sprintf("%s:%d", a, e.GetPort())
}

// packetLine renders one reply.
//
//declscope:package // ping and watch print replies
func packetLine(pr *gotwampv1.PacketResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seq=%-3d", pr.GetSeq())
	if pr.GetRtt() == nil {
		b.WriteString(" (timestamps unusable)")
	} else {
		fmt.Fprintf(&b, " rtt=%s  fwd=%s  rev=%s  refl=%s",
			ms(pr.GetRtt()), ms(pr.GetFwdDelay()), ms(pr.GetRevDelay()), us(pr.GetReflectorDelay()))
	}
	fmt.Fprintf(&b, "  ttl=%d/%d", pr.GetSenderTtlAtReflector(), pr.GetReflectorTtlReceived())
	if pr.GetRtt() != nil && !pr.GetOwdValid() {
		b.WriteString(" (unsync)")
	}
	switch pr.GetDuplicateKind() {
	case gotwampv1.DuplicateKind_DUPLICATE_KIND_FORWARD:
		b.WriteString("  DUP (forward)")
	case gotwampv1.DuplicateKind_DUPLICATE_KIND_REVERSE:
		b.WriteString("  DUP (reverse)")
	}
	if pr.GetReordered() {
		b.WriteString("  reordered")
	}
	return b.String()
}

// summaryText renders the summary block of SPEC §10.3.
//
//declscope:package // ping and session show print summaries
func summaryText(target string, s *gotwampv1.RunSummary) string {
	var b strings.Builder
	title := "TWAMP statistics"
	if s.GetAborted() {
		title += " (aborted)"
	}
	if s.GetInvalid() {
		title += " (invalid: the control connection failed)"
	}
	fmt.Fprintf(&b, "--- %s %s ---\n", target, title)
	fmt.Fprintf(&b, "%d sent, %d lost (%.3f%%), %d dup (fwd %d / rev %d), %d reordered\n",
		s.GetSent(), s.GetLost(), 100*s.GetLossRatio(), s.GetDupForward()+s.GetDupReverse(),
		s.GetDupForward(), s.GetDupReverse(), s.GetReordered())
	rtt := s.GetRtt()
	if rtt.GetSamples() == 0 {
		b.WriteString("rtt      no replies\n")
		return b.String()
	}
	fmt.Fprintf(&b, "rtt      min/median/mean/max = %s/%s/%s/%s ms, p95 %s ms, stddev %s ms\n",
		msN(rtt.GetMin()), msN(rtt.GetMedian()), msN(rtt.GetMean()), msN(rtt.GetMax()), msN(rtt.GetP95()), msN(rtt.GetStddev()))
	clk := s.GetClock()
	fwdNote := fmt.Sprintf("   (clocks synchronized, max error %s ms)", msN(clk.GetMaxClockError()))
	if clk.GetMaxClockErrorUnknown() {
		fwdNote = "   (clocks synchronized, max error unknown)"
	}
	var revNote string
	if !s.GetOwdValid() {
		// Both one-way delays are invalid (SPEC §10.3).
		fwdNote = "   (unsync: clocks not synchronized)"
		revNote = fwdNote
	}
	fwd, rev := s.GetFwdDelay(), s.GetRevDelay()
	fmt.Fprintf(&b, "forward  min/median/max = %s/%s/%s ms%s\n", msN(fwd.GetMin()), msN(fwd.GetMedian()), msN(fwd.GetMax()), fwdNote)
	fmt.Fprintf(&b, "reverse  min/median/max = %s/%s/%s ms%s\n", msN(rev.GetMin()), msN(rev.GetMedian()), msN(rev.GetMax()), revNote)
	refl := s.GetReflectorDelay()
	fmt.Fprintf(&b, "reflector processing min/max = %s/%s us\n", usN(refl.GetMin()), usN(refl.GetMax()))
	j := s.GetJitter()
	fmt.Fprintf(&b, "jitter   ipdv mean %s ms, max %s ms; pdv p95 %s ms; rfc3550 %s ms\n",
		msN(j.GetIpdvRttMean()), msN(j.GetIpdvRttMax()), msN(j.GetPdvRttP95()), msN(j.GetJitterRfc3550()))
	fmt.Fprintf(&b, "hops     forward %d, reverse %d\n", s.GetHops().GetFwd(), s.GetHops().GetRev())
	if n := s.GetReflectedMismatch(); n > 0 {
		fmt.Fprintf(&b, "reflect octets: %d replies did not return the padding asked to be reflected\n", n)
	}
	ts := strings.ToLower(strings.TrimPrefix(clk.GetTimestampingMode().String(), "TIMESTAMPING_MODE_"))
	fmt.Fprintf(&b, "timestamps: %s (sender), reflector synced: %s\n", ts, yesNo(clk.GetReflectorSynced()))
	return b.String()
}

//declscope:package // ping, watch and session print run errors
func stageName(s gotwampv1.RunStage) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "RUN_STAGE_"))
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// ms formats a duration in milliseconds with its unit, "-" when unset.
func ms(d *durationpb.Duration) string {
	if d == nil {
		return "-"
	}
	return msN(d) + "ms"
}

// us formats a duration in microseconds with its unit.
func us(d *durationpb.Duration) string {
	if d == nil {
		return "-"
	}
	return usN(d) + "us"
}

//declscope:package // stats prints delays too
func msN(d *durationpb.Duration) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", float64(d.AsDuration())/float64(time.Millisecond))
}

func usN(d *durationpb.Duration) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprintf("%d", d.AsDuration()/time.Microsecond)
}

// summaryLine is a one-line summary.
//
//declscope:package // session list shows the latest result with it
func summaryLine(s *gotwampv1.RunSummary) string {
	line := fmt.Sprintf("%d/%d received, loss %.3f%%", s.GetReceived(), s.GetSent(), 100*s.GetLossRatio())
	if r := s.GetRtt(); r.GetSamples() > 0 {
		line += fmt.Sprintf(", rtt median %s ms", msN(r.GetMedian()))
	}
	if s.GetAborted() {
		line += " (aborted)"
	}
	return line
}

// table writes aligned columns.
type table struct {
	tw *tabwriter.Writer
}

// newTable starts a table with its header.
//
//declscope:package // every listing command prints tables
func newTable(w io.Writer, header ...string) *table {
	t := &table{tw: tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)}
	t.row(header...)
	return t
}

// row adds a row.
//
//declscope:package // every listing command prints tables
func (t *table) row(cells ...string) {
	for i, c := range cells {
		if i > 0 {
			_, _ = io.WriteString(t.tw, "\t")
		}
		_, _ = io.WriteString(t.tw, c)
	}
	_, _ = io.WriteString(t.tw, "\n")
}

// flush writes the table out.
//
//declscope:package // every listing command prints tables
func (t *table) flush() error { return t.tw.Flush() }

// sorted returns s sorted.
//
//declscope:package // session show sorts labels
func sorted(s []string) []string {
	slices.Sort(s)
	return s
}
