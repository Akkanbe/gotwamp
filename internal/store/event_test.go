package store

import "testing"

type countSink struct{ n int }

func (c *countSink) Publish(Event) { c.n++ }

func TestFanout(t *testing.T) {
	a, b := &countSink{}, &countSink{}
	f := Fanout{a, b}
	f.Publish(Event{})
	f.Publish(Event{})
	if a.n != 2 || b.n != 2 {
		t.Errorf("sinks got %d and %d events", a.n, b.n)
	}
}

func TestEventKindString(t *testing.T) {
	for k, want := range map[EventKind]string{
		EventRunSummary:    "run_summary",
		EventWindowSummary: "window_summary",
		EventRunError:      "run_error",
		EventRunSkipped:    "run_skipped",
		EventPacket:        "packet",
		EventLost:          "lost",
		0:                  "unknown",
	} {
		if got := k.String(); got != want {
			t.Errorf("%d: %q, want %q", k, got, want)
		}
	}
}
