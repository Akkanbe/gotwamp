package daemon

import (
	"sync"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// eventQueueSize is how many per-packet events a RunTest stream may lag
// behind its run before they are dropped.
const eventQueueSize = 1024

// eventQueue hands the events of an ad-hoc run from the run's goroutines to
// the RPC handler, which alone sends on the stream. Queueing never blocks,
// so that neither a slow client nor a stalled stream holds up the run's
// receive loop, whose timestamps it would distort, or keeps StopTest and
// cancellation from ending the run (SPEC §4.4, §6.5). Per-packet events
// beyond eventQueueSize are dropped and reported with an EventsDropped
// event; the few others (start, summary, error) are always kept.
type eventQueue struct {
	mu      sync.Mutex
	events  []*gotwampv1.TestEvent
	packets int    // per-packet events in events
	dropped uint64 // per-packet events dropped since the last take
	runID   string
	notify  chan struct{}
}

// newEventQueue returns an empty queue for the events of run runID.
//
//declscope:package // RunTest streams through it
func newEventQueue(runID string) *eventQueue {
	return &eventQueue{runID: runID, notify: make(chan struct{}, 1)}
}

// offer queues a per-packet event, or drops it when eventQueueSize of them
// are waiting.
//
//declscope:package // the packet and loss hooks of RunTest
func (q *eventQueue) offer(ev *gotwampv1.TestEvent) {
	q.mu.Lock()
	if q.packets >= eventQueueSize {
		q.dropped++
		q.mu.Unlock()
		return
	}
	q.packets++
	q.add(ev)
}

// put queues an event that must not be dropped.
//
//declscope:package // RunTest's start, summary and error events
func (q *eventQueue) put(ev *gotwampv1.TestEvent) {
	q.mu.Lock()
	q.add(ev)
}

// add appends ev, unlocks and wakes the handler. q.mu is held.
func (q *eventQueue) add(ev *gotwampv1.TestEvent) {
	ev.RunId = q.runID
	q.events = append(q.events, ev)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// ready is signaled when events are waiting.
//
//declscope:package // RunTest waits on it
func (q *eventQueue) ready() <-chan struct{} { return q.notify }

// take returns the waiting events, preceded by an EventsDropped event when
// per-packet events were dropped.
//
//declscope:package // RunTest sends what it takes
func (q *eventQueue) take() []*gotwampv1.TestEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	evs := q.events
	if q.dropped > 0 {
		drop := &gotwampv1.TestEvent{RunId: q.runID, Event: &gotwampv1.TestEvent_Dropped{Dropped: &gotwampv1.EventsDropped{Count: q.dropped}}}
		evs = append([]*gotwampv1.TestEvent{drop}, evs...)
	}
	q.events, q.packets, q.dropped = nil, 0, 0
	return evs
}
