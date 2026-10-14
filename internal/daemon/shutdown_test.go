package daemon

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// farEnd starts a TWAMP-Control server with a Full reflector that records
// the control messages it receives, and returns its control port.
func farEnd(t *testing.T) (*server.Server, *recordingListener, int) {
	t.Helper()
	full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{}, full, nil)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &recordingListener{Listener: inner}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() { _ = srv.Serve(ctx, ln) }()
	return srv, ln, inner.Addr().(*net.TCPAddr).Port
}

// recordingListener keeps what every client sent on its control connection.
type recordingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []*recordingConn
}

type recordingConn struct {
	net.Conn
	mu  sync.Mutex
	got bytes.Buffer
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	rc := &recordingConn{Conn: c}
	l.mu.Lock()
	l.conns = append(l.conns, rc)
	l.mu.Unlock()
	return rc, nil
}

func (c *recordingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.mu.Lock()
	c.got.Write(b[:n])
	c.mu.Unlock()
	return n, err
}

// stopSessions returns the Stop-Sessions the client sent after one session
// (Set-Up-Response, Request-TW-Session, Start-Sessions, Stop-Sessions).
func (c *recordingConn) stopSessions() (wire.StopSessions, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.got.Bytes()
	off := wire.SetUpResponseSize + wire.RequestTWSessionSize + wire.StartSessionsSize
	var stop wire.StopSessions
	if len(b) < off+wire.StopSessionsSize {
		return stop, fmt.Errorf("client sent %d octets, no Stop-Sessions", len(b))
	}
	return stop, stop.Decode(b[off:])
}

// TestShutdownStopsFullSessions: when gotwampd stops (SIGTERM ends the
// context of daemon.Run), a running ad-hoc test and a running monitoring
// session both send Stop-Sessions before their control connections close
// (SPEC §9.1, §4.4, NFR-05).
func TestShutdownStopsFullSessions(t *testing.T) {
	srv, ln, port := farEnd(t)

	// The daemon under test: a long Full session and a long ad-hoc Full test.
	c := testConfig(t)
	c.Reflector.Enabled = false
	c.Sender.Sessions = []config.Session{{
		Name: "long-full", Target: "127.0.0.1",
		Schedule: config.Schedule{Type: "periodic", Frequency: 10 * time.Minute},
		TestParams: func() config.TestParams {
			p := c.Sender.Defaults
			p.Mode, p.ControlPort, p.Count, p.Interval, p.Timeout = "full", port, 10_000, 10*time.Millisecond, time.Second
			return p
		}(),
	}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	r := start(t, c, Options{})
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: &gotwampv1.TestSpec{
		Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(port), Count: proto.Uint32(10_000),
		Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(time.Second),
	}})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan *gotwampv1.TestEvent, 100_000)
	go func() {
		defer close(events)
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			events <- ev
		}
	}()
	waitFor(t, "both Full sessions to reflect", func() bool {
		n := 0
		for _, s := range srv.Sessions() {
			if s.State == server.SessionActive && s.Stats.Reflected > 0 {
				n++
			}
		}
		return n == 2
	})

	begin := time.Now()
	r.stop(t) // what SIGTERM does through daemon.Run's context
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("shutdown took %v, want under 5s (NFR-05)", d)
	}

	ln.mu.Lock()
	conns := append([]*recordingConn(nil), ln.conns...)
	ln.mu.Unlock()
	if len(conns) != 2 {
		t.Fatalf("%d control connections, want 2", len(conns))
	}
	for i, rc := range conns {
		stop, err := rc.stopSessions()
		if err != nil {
			t.Errorf("connection %d: %v", i, err)
			continue
		}
		if stop.Sessions != 1 || stop.Accept != wire.AcceptOK {
			t.Errorf("connection %d: Stop-Sessions for %d sessions with accept %v, want 1 and OK", i, stop.Sessions, stop.Accept)
		}
	}
	// The far end saw the stop: the sessions drain instead of reflecting on.
	for _, s := range srv.Sessions() {
		if s.State == server.SessionActive {
			t.Errorf("session %x still active after the daemon stopped", s.SID)
		}
	}

	// The ad-hoc stream ends with the aborted summary and the reason.
	var aborted, reason bool
	for ev := range events {
		if s := ev.GetSummary(); s != nil && s.GetAborted() && len(s.GetSid()) == 16 {
			aborted = true
		}
		if e := ev.GetError(); e != nil && e.GetMessage() == "gotwampd is shutting down" {
			reason = true
		}
	}
	if !aborted || !reason {
		t.Errorf("ad-hoc stream: aborted summary with SID %v, shutdown error %v", aborted, reason)
	}
}

// TestStalledFullRunStops: a Full ad-hoc test whose client has stopped
// reading still ends, sends Stop-Sessions and frees its slot, whether
// StopTest, the client going away or the daemon's shutdown ends it (Codex
// M1, SPEC §4.4).
func TestStalledFullRunStops(t *testing.T) {
	for _, how := range []string{"StopTest", "disconnect", "shutdown"} {
		t.Run(how, func(t *testing.T) {
			_, ln, port := farEnd(t)
			c := testConfig(t)
			c.Reflector.Enabled = false
			c.Daemon.Limits.MaxConcurrentTests = 1
			r := start(t, c, Options{})
			stopped := false
			defer func() {
				if !stopped {
					r.stop(t)
				}
			}()
			client := gotwampv1.NewSenderServiceClient(r.conn)
			spec := func(count uint32, interval time.Duration) *gotwampv1.TestSpec {
				return &gotwampv1.TestSpec{
					Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(port), Count: proto.Uint32(count),
					Interval: durationpb.New(interval), Timeout: durationpb.New(time.Second),
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := client.RunTest(ctx, &gotwampv1.RunTestRequest{Spec: spec(100_000, time.Millisecond)})
			if err != nil {
				t.Fatal(err)
			}
			first, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			// Read nothing more: the stream's flow control fills up.
			time.Sleep(1500 * time.Millisecond)

			switch how {
			case "StopTest":
				if _, err := client.StopTest(t.Context(), &gotwampv1.StopTestRequest{RunId: first.GetRunId()}); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				cancel()
			case "shutdown":
				begin := time.Now()
				r.stop(t)
				stopped = true
				if d := time.Since(begin); d > 5*time.Second {
					t.Errorf("shutdown took %v, want under 5s", d)
				}
			}
			waitFor(t, "Stop-Sessions", func() bool {
				ln.mu.Lock()
				defer ln.mu.Unlock()
				if len(ln.conns) != 1 {
					return false
				}
				stop, err := ln.conns[0].stopSessions()
				return err == nil && stop.Sessions == 1 && stop.Accept == wire.AcceptOK
			})
			if stopped {
				return
			}
			waitFor(t, "the slot to be freed", func() bool {
				s, err := client.RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec(1, 10*time.Millisecond)})
				if err != nil {
					return false
				}
				for {
					ev, err := s.Recv()
					if err != nil {
						return false
					}
					if ev.GetSummary() != nil {
						return true
					}
				}
			})
		})
	}
}
