//declscope:namespace sendersvc // tests of the RunTest and WatchResults limits

package daemon

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// TestConcurrencyLimits: daemon.limits.max_concurrent_tests and
// max_watchers refuse the request beyond them, and free the slot when a
// test or a watch ends.
func TestConcurrencyLimits(t *testing.T) {
	c := testConfig(t)
	c.Daemon.Limits.MaxConcurrentTests, c.Daemon.Limits.MaxWatchers = 1, 1
	r := start(t, c, Options{})
	defer r.stop(t)
	client := gotwampv1.NewSenderServiceClient(r.conn)
	spec := lightSpec(r.d.LightAddrs()[0], 100_000)

	first, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.Recv()
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err == nil {
		_, err = second.Recv()
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("second RunTest = %v, want ResourceExhausted", err)
	}
	if _, err := client.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: started.GetRunId()}); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := first.Recv(); err != nil {
			break
		}
	}
	short := lightSpec(r.d.LightAddrs()[0], 1)
	short.Count = proto.Uint32(1)
	short.Interval = durationpb.New(time.Millisecond)
	if evs := collect(t, mustRunTest(t, client, short)); len(evs) == 0 {
		t.Error("RunTest after the first ended got no events")
	}

	// A long watch takes the only slot; a probe must then be refused. The
	// two may reach the server in either order, so a long watch that was
	// itself refused is opened again.
	var ended chan error
	open := func() {
		w1, err := client.WatchResults(t.Context(), &gotwampv1.WatchResultsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		ended = make(chan error, 1)
		go func(done chan error) { // a watcher reads its stream, as the CLI does
			for {
				if _, err := w1.Recv(); err != nil {
					done <- err
					return
				}
			}
		}(ended)
	}
	open()
	waitFor(t, "a second WatchResults to be refused", func() bool {
		select {
		case <-ended:
			open()
		default:
		}
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		w2, err := client.WatchResults(ctx, &gotwampv1.WatchResultsRequest{})
		if err == nil {
			_, err = w2.Recv()
		}
		return status.Code(err) == codes.ResourceExhausted
	})
}

func mustRunTest(t *testing.T, client gotwampv1.SenderServiceClient, spec *gotwampv1.TestSpec) gotwampv1.SenderService_RunTestClient {
	t.Helper()
	s, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
