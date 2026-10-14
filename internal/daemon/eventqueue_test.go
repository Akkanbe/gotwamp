package daemon

import (
	"testing"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func TestEventQueue(t *testing.T) {
	q := newEventQueue("01RUN")
	q.put(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Started{Started: &gotwampv1.RunStarted{}}})
	for range eventQueueSize + 10 {
		q.offer(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Packet{Packet: &gotwampv1.PacketResult{}}})
	}
	q.put(&gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Summary{Summary: &gotwampv1.RunSummary{}}})
	select {
	case <-q.ready():
	default:
		t.Fatal("not signaled")
	}
	evs := q.take()
	if len(evs) != 1+1+eventQueueSize+1 {
		t.Fatalf("%d events", len(evs))
	}
	if d := evs[0].GetDropped(); d.GetCount() != 10 {
		t.Errorf("first event %v, want 10 dropped", evs[0])
	}
	if evs[1].GetStarted() == nil || evs[len(evs)-1].GetSummary() == nil {
		t.Error("the start and the summary were not kept")
	}
	for _, ev := range evs {
		if ev.GetRunId() != "01RUN" {
			t.Fatalf("event without the run ID: %v", ev)
		}
	}
	// After a take the per-packet room is free again.
	q.offer(&gotwampv1.TestEvent{})
	if evs := q.take(); len(evs) != 1 || evs[0].GetDropped() != nil {
		t.Errorf("after take: %v", evs)
	}
}
