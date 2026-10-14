package cli

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// TestSummaryClockNotes: an unsynchronized run marks both one-way delay
// lines (SPEC §10.3), and an unknown error bound is not shown as a number.
func TestSummaryClockNotes(t *testing.T) {
	s := &gotwampv1.RunSummary{
		Sent: 1, Received: 1,
		Rtt:   &gotwampv1.DelayStats{Samples: 1, Min: durationpb.New(time.Millisecond)},
		Clock: &gotwampv1.ClockInfo{MaxClockError: durationpb.New(time.Millisecond)},
	}
	out := summaryText("x", s)
	for _, line := range strings.Split(out, "\n") {
		if (strings.HasPrefix(line, "forward") || strings.HasPrefix(line, "reverse")) && !strings.HasSuffix(line, "(unsync: clocks not synchronized)") {
			t.Errorf("unsync run: %q", line)
		}
	}
	s.OwdValid, s.Clock.MaxClockErrorUnknown = true, true
	if out := summaryText("x", s); !strings.Contains(out, "(clocks synchronized, max error unknown)") || strings.Contains(out, "unsync") {
		t.Errorf("unknown error bound:\n%s", out)
	}
}
