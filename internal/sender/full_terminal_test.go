//declscope:namespace run // the Full tests run over the in-memory link of run_test.go

package sender

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// terminalConn observes Stop before its write can complete, independently of
// summary callback timing. It can fail that write like a broken TCP connection.
// stopMsg keeps the Stop message written, or attempted, last.
type terminalConn struct {
	net.Conn
	stop    chan struct{}
	once    sync.Once
	stopErr error
	stopMsg []byte
}

func (c *terminalConn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(senderAddr) }
func (c *terminalConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(reflectorAddr) }
func (c *terminalConn) Write(b []byte) (int, error) {
	if cmd := wire.Command(b[0]); cmd == wire.CommandStopSessions || cmd == wire.CommandStopNSessions {
		c.stopMsg = append([]byte(nil), b...)
		c.once.Do(func() { close(c.stop) })
		if c.stopErr != nil {
			return 0, c.stopErr
		}
	}
	return c.Conn.Write(b)
}

func terminalConfig(t *testing.T, isc bool, l tstamp.Conn, stopErr error, finish func(net.Conn, <-chan struct{})) (FullConfig, func() error) {
	t.Helper()
	a, b := net.Pipe()
	c := &terminalConn{Conn: a, stop: make(chan struct{}), stopErr: stopErr}
	done := make(chan struct{})
	sid := wire.SID{1}
	features := wire.Modes(0)
	if isc {
		features = wire.ModeIndividualSessionControl
	}
	go func() {
		defer close(done)
		defer b.Close()
		write := func(m interface{ Append([]byte) []byte }) { _, _ = b.Write(m.Append(nil)) }
		read := func(n int) { _, _ = io.ReadFull(b, make([]byte, n)) }
		write(&wire.ServerGreeting{Modes: wire.ModeUnauthenticated | features, Count: 2048})
		read(wire.SetUpResponseSize)
		write(&wire.ServerStart{})
		read(wire.RequestTWSessionSize)
		write(&wire.AcceptSession{Port: reflectorAddr.Port(), SID: sid})
		if isc {
			read(wire.SessionControlSize(1))
			write(&wire.SessionControl{Command: wire.CommandStartNAck, SIDs: []wire.SID{sid}})
		} else {
			read(wire.StartSessionsSize)
			write(&wire.StartAck{})
		}
		finish(b, c.stop)
	}()
	t.Cleanup(func() { c.once.Do(func() { close(c.stop) }); _ = a.Close(); _ = b.Close(); <-done })
	return FullConfig{
		Server: reflectorAddr, Features: features, ControlTimeout: 100 * time.Millisecond,
		Dial:   func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) { return c, nil },
		Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return l, nil },
	}, b.Close
}

func TestFullTerminalState(t *testing.T) {
	for _, isc := range []bool{false, true} {
		name := "ordinary"
		if isc {
			name = "isc"
		}
		for _, event := range []string{"success", "cancel_measurement", "custom_cancel", "loss_measurement", "socket_failure", "cancel_delay", "loss_delay", "stop_failure", "cancel_stop"} {
			t.Run(name+"/"+event, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancelCause(t.Context())
					defer cancel(nil)
					failure := errors.New("injected transport failure")
					custom := errors.New("caller stopped the run")
					l := newLink()
					var stopErr error
					if event == "stop_failure" {
						stopErr = failure
					}
					fc, closeControl := terminalConfig(t, isc, l, stopErr, func(peer net.Conn, stopped <-chan struct{}) {
						<-stopped
						if event == "cancel_stop" {
							cancel(nil)
						}
						if stopErr != nil {
							_, _ = io.Copy(io.Discard, peer)
							return
						}
						if isc {
							_, _ = io.ReadFull(peer, make([]byte, wire.SessionControlSize(1)))
							_, _ = peer.Write((&wire.SessionControl{Command: wire.CommandStopNAck, SIDs: []wire.SID{{1}}}).Append(nil))
						} else {
							var stop wire.StopSessions
							buf := make([]byte, wire.StopSessionsSize)
							if _, err := io.ReadFull(peer, buf); err == nil {
								_ = stop.Decode(buf)
								want := wire.AcceptOK
								if event == "socket_failure" {
									want = wire.AcceptFailure
								}
								if stop.Accept != want {
									t.Errorf("Stop Accept=%v, want %v", stop.Accept, want)
								}
							}
						}
					})
					cfg := testConfig(1)
					if event == "cancel_measurement" || event == "custom_cancel" || event == "loss_measurement" {
						cfg.Count = 10
					}
					if event == "success" || event == "cancel_delay" || event == "loss_delay" {
						fc.EndDelay = time.Second
					}
					if event == "socket_failure" {
						l.readErr = failure
					}
					var summaries []Summary
					var packetAt, callbackAt time.Time
					hooks := Hooks{
						Packet: func(p PacketResult) {
							if p.Result.Kind != stats.KindAccepted {
								return
							}
							packetAt = time.Now()
							if event == "cancel_measurement" {
								cancel(nil)
							}
							if event == "custom_cancel" {
								cancel(custom)
							}
						},
						Summary: func(s Summary) { summaries = append(summaries, s); callbackAt = time.Now() },
					}
					packet := hooks.Packet
					hooks.Packet = func(p PacketResult) {
						packet(p)
						if event == "loss_measurement" {
							_ = closeControl()
						}
						if event == "cancel_delay" {
							time.AfterFunc(20*time.Millisecond, func() { cancel(nil) })
						}
						if event == "loss_delay" {
							time.AfterFunc(20*time.Millisecond, func() { _ = closeControl() })
						}
					}
					sum, err := RunFull(ctx, cfg, fc, WithHooks(hooks))
					wantInvalid := event == "loss_measurement" || event == "loss_delay" || event == "stop_failure"
					switch event {
					case "success":
						if err != nil {
							t.Error(err)
						}
						if callbackAt.Before(packetAt.Add(fc.EndDelay)) {
							t.Error("bounded callback preceded teardown")
						}
					case "cancel_delay", "cancel_stop":
						// Every packet was decided: the cancellation only
						// cuts EndDelay short.
						if err != nil {
							t.Error(err)
						}
					case "cancel_measurement", "custom_cancel":
						want := error(context.Canceled)
						if event == "custom_cancel" {
							want = custom
						}
						if !errors.Is(err, want) {
							t.Errorf("error=%v, want %v", err, want)
						}
					case "loss_measurement", "loss_delay":
						if !errors.Is(err, client.ErrControlLost) {
							t.Errorf("error=%v, want control loss", err)
						}
						if event == "loss_delay" && time.Since(packetAt) >= fc.EndDelay {
							t.Error("control loss did not interrupt EndDelay")
						}
					default:
						if !errors.Is(err, failure) {
							t.Errorf("error=%v, want transport failure", err)
						}
					}
					if sum.Invalid != wantInvalid {
						t.Errorf("Invalid=%v, want %v; error=%v", sum.Invalid, wantInvalid, err)
					}
					wantAborted := event == "cancel_measurement" || event == "custom_cancel" || event == "loss_measurement" || event == "socket_failure"
					if sum.Aborted != wantAborted {
						t.Errorf("Aborted=%v, want %v", sum.Aborted, wantAborted)
					}
					if !wantAborted && (sum.Sent != 1 || sum.Received != 1 || sum.Pending != 0) {
						t.Errorf("completed measurement changed: %+v", sum)
					}
					if len(summaries) != 1 || !reflect.DeepEqual(summaries[0], sum) {
						t.Errorf("callback=%+v, returned=%+v", summaries, sum)
					}
				})
			})
		}
	}
}

func TestFullStopTimeoutFinalSummary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		fc, _ := terminalConfig(t, true, l, nil, func(peer net.Conn, stopped <-chan struct{}) {
			<-stopped
			_, _ = io.ReadFull(peer, make([]byte, wire.SessionControlSize(1)))
			_, _ = io.Copy(io.Discard, peer) // withhold Stop-N-Ack until timeout closes TCP
		})
		var hooked []Summary
		sum, err := RunFull(t.Context(), testConfig(1), fc, WithHooks(Hooks{Summary: func(s Summary) { hooked = append(hooked, s) }}))
		if !errors.Is(err, client.ErrTimeout) || !sum.Invalid || sum.Aborted || sum.Pending != 0 || sum.Received != 1 {
			t.Errorf("summary=%+v error=%v", sum, err)
		}
		if len(hooked) != 1 || !reflect.DeepEqual(hooked[0], sum) {
			t.Errorf("hooked=%+v returned=%+v", hooked, sum)
		}
	})
}

func TestFullContinuousCompletedWindowsSurviveFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		fc, closeControl := terminalConfig(t, false, l, nil, func(_ net.Conn, stopped <-chan struct{}) { <-stopped })
		cfg := testConfig(0)
		cfg.Interval, cfg.Window = 50*time.Millisecond, 100*time.Millisecond
		var hooked []Summary
		sum, err := RunFull(t.Context(), cfg, fc, WithHooks(Hooks{
			Summary: func(s Summary) {
				hooked = append(hooked, s)
				if len(hooked) == 1 {
					_ = closeControl() // once the first window is reported
				}
			},
		}))
		if !errors.Is(err, client.ErrControlLost) || !sum.Invalid || !sum.Aborted {
			t.Errorf("summary=%+v error=%v", sum, err)
		}
		if len(hooked) != 2 {
			t.Fatalf("summaries=%+v", hooked)
		}
		if hooked[0].Invalid || hooked[0].Aborted || hooked[0].Received != 2 {
			t.Errorf("completed window changed: %+v", hooked[0])
		}
		if !reflect.DeepEqual(hooked[1], sum) {
			t.Errorf("final window=%+v returned=%+v", hooked[1], sum)
		}
	})
}

func TestFullContinuousAbortKeepsEveryOpenWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(uint32) behavior { return behavior{drop: true} }
		fc, _ := terminalConfig(t, false, l, nil, func(peer net.Conn, stopped <-chan struct{}) {
			<-stopped
			_, _ = io.CopyN(io.Discard, peer, wire.StopSessionsSize)
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cfg := testConfig(0)
		cfg.Interval, cfg.Window, cfg.Timeout = 50*time.Millisecond, 100*time.Millisecond, time.Second
		time.AfterFunc(250*time.Millisecond, cancel)
		var hooked []Summary
		sum, err := RunFull(ctx, cfg, fc, WithHooks(Hooks{Summary: func(s Summary) { hooked = append(hooked, s) }}))
		if !errors.Is(err, context.Canceled) || len(hooked) != 3 {
			t.Fatalf("error=%v, %d summaries", err, len(hooked))
		}
		for i, s := range hooked {
			if s.Window != i || !s.Aborted || s.Invalid || s.Lost != 0 || s.Pending != s.Sent {
				t.Errorf("window %d: %+v", i, s)
			}
		}
		if !reflect.DeepEqual(sum, hooked[len(hooked)-1]) {
			t.Error("final open window differs from returned summary")
		}
	})
}

func TestFullContinuousTerminalStateWithoutPendingPackets(t *testing.T) {
	for _, event := range []string{"control_loss", "cancel_with_stop_failure"} {
		for _, withSummary := range []bool{false, true} {
			name := event + "/without_summary_hook"
			if withSummary {
				name = event + "/with_summary_hook"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					var stopErr error
					if event == "cancel_with_stop_failure" {
						stopErr = errors.New("stop failed after completed window")
					}
					fc, closeControl := terminalConfig(t, false, newLink(), stopErr, func(peer net.Conn, stopped <-chan struct{}) { <-stopped; _, _ = io.Copy(io.Discard, peer) })
					cfg := testConfig(0)
					cfg.Interval, cfg.Window = 100*time.Millisecond, 50*time.Millisecond
					// The current window stays open until the next one starts,
					// even when every packet sent so far already has a reply.
					var hooked []Summary
					hooks := Hooks{Packet: func(PacketResult) {
						time.AfterFunc(20*time.Millisecond, func() {
							if event == "control_loss" {
								_ = closeControl()
							} else {
								cancel()
							}
						})
					}}
					if withSummary {
						hooks.Summary = func(s Summary) { hooked = append(hooked, s) }
					}
					sum, err := RunFull(ctx, cfg, fc, WithHooks(hooks))
					want := error(client.ErrControlLost)
					if event == "cancel_with_stop_failure" {
						want = context.Canceled
					}
					if !errors.Is(err, want) {
						t.Errorf("error=%v want=%v", err, want)
					}
					if !sum.Invalid || !sum.Aborted || sum.Received != 1 || sum.Pending != 0 {
						t.Errorf("open window classification: %+v", sum)
					}
					if withSummary && (len(hooked) != 1 || !reflect.DeepEqual(sum, hooked[0])) {
						t.Errorf("hooked=%+v returned=%+v", hooked, sum)
					}
				})
			})
		}
	}
}

// faultyLink is a link whose reads fail once failing is set. before runs
// just before the first failed read returns; after runs when the run, having
// seen the failure, clears its read deadline, that is once the receiver has
// returned the error and before Run does.
type faultyLink struct {
	*link
	mu            sync.Mutex
	failing       bool
	err           error
	before, after func()
}

func (f *faultyLink) fail() {
	f.mu.Lock()
	f.failing = true
	f.mu.Unlock()
}

func (f *faultyLink) ReadFrom(b []byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	f.mu.Lock()
	failing, before := f.failing, f.before
	if failing {
		f.before = nil
	}
	f.mu.Unlock()
	if !failing {
		return f.link.ReadFrom(b)
	}
	if before != nil {
		before()
	}
	return 0, netip.AddrPort{}, tstamp.RxMeta{}, f.err
}

func (f *faultyLink) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	var after func()
	if f.failing && t.IsZero() {
		after, f.after = f.after, nil
	}
	f.mu.Unlock()
	if after != nil {
		after() // without the lock: it may wait for goroutines that take it
	}
	return f.link.SetReadDeadline(t)
}

// TestFullFailurePriority pins how RunFull ranks the failures that meet in
// one run: a lost control connection, then a failure of the measurement,
// then the caller's cancellation, then a failed Stop-Sessions. Only a lost
// connection and a failed Stop-Sessions mark the results invalid.
func TestFullFailurePriority(t *testing.T) {
	for _, isc := range []bool{false, true} {
		name := "ordinary"
		if isc {
			name = "isc"
		}
		for _, event := range []string{
			"socket_failure_then_cancel", "cancel_then_socket_failure", "socket_and_stop_failure",
			"socket_failure_and_control_loss", "cancel_delay_and_stop_failure",
		} {
			t.Run(name+"/"+event, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					socketErr := errors.New("injected socket failure")
					var stopErr error
					if event == "socket_and_stop_failure" || event == "cancel_delay_and_stop_failure" {
						stopErr = errors.New("injected stop failure")
					}
					l := &faultyLink{link: newLink(), err: socketErr}
					fc, closeControl := terminalConfig(t, isc, l, stopErr, func(peer net.Conn, stopped <-chan struct{}) {
						<-stopped
						if stopErr == nil && isc {
							_, _ = io.ReadFull(peer, make([]byte, wire.SessionControlSize(1)))
							_, _ = peer.Write((&wire.SessionControl{Command: wire.CommandStopNAck, SIDs: []wire.SID{{1}}}).Append(nil))
						}
						_, _ = io.Copy(io.Discard, peer)
					})
					var tc *terminalConn
					dial := fc.Dial
					fc.Dial = func(ctx context.Context, local netip.Addr, server netip.AddrPort) (net.Conn, error) {
						conn, err := dial(ctx, local, server)
						tc, _ = conn.(*terminalConn)
						return conn, err
					}
					switch event {
					case "socket_failure_then_cancel":
						l.after = cancel
					case "cancel_then_socket_failure":
						l.before = cancel
					case "socket_failure_and_control_loss":
						l.after = func() {
							_ = closeControl()
							synctest.Wait() // the client has seen the connection close
						}
					}
					cfg := testConfig(10)
					if event == "cancel_delay_and_stop_failure" {
						cfg.Count = 1
						fc.EndDelay = time.Second
					}
					var summaries []Summary
					hooks := Hooks{
						Packet: func(p PacketResult) {
							if p.Result.Kind != stats.KindAccepted {
								return
							}
							if event == "cancel_delay_and_stop_failure" {
								time.AfterFunc(20*time.Millisecond, cancel)
								return
							}
							l.fail()
						},
						Summary: func(s Summary) { summaries = append(summaries, s) },
					}
					sum, err := RunFull(ctx, cfg, fc, WithHooks(hooks))
					// A hook that never ran, or a cancellation that never came,
					// would leave some cases passing for the wrong reason.
					l.mu.Lock()
					pending := l.before != nil || l.after != nil
					l.mu.Unlock()
					canceled := event == "socket_failure_then_cancel" || event == "cancel_then_socket_failure" || event == "cancel_delay_and_stop_failure"
					if pending || canceled != (ctx.Err() != nil) {
						t.Fatalf("injection did not run: hook pending %v, canceled %v", pending, ctx.Err() != nil)
					}

					var want error
					wantAccept, wantInvalid, wantAborted := wire.AcceptFailure, false, true
					switch event {
					case "socket_failure_then_cancel":
						want = socketErr
					case "cancel_then_socket_failure":
						// The receiver saw the cancellation first: the failed
						// read is how the cancellation ended it.
						want, wantAccept = context.Canceled, wire.AcceptOK
					case "socket_and_stop_failure":
						want, wantInvalid = socketErr, true
					case "socket_failure_and_control_loss":
						want, wantInvalid = client.ErrControlLost, true
					case "cancel_delay_and_stop_failure":
						want, wantAccept, wantInvalid, wantAborted = stopErr, wire.AcceptOK, true, false
					}
					if !errors.Is(err, want) {
						t.Errorf("error=%v, want %v", err, want)
					}
					for _, other := range []error{socketErr, stopErr, context.Canceled} {
						if other != nil && other != want && errors.Is(err, other) {
							t.Errorf("error=%v also matches %v", err, other)
						}
					}
					if sum.Invalid != wantInvalid || sum.Aborted != wantAborted {
						t.Errorf("Invalid=%v Aborted=%v, want %v %v", sum.Invalid, sum.Aborted, wantInvalid, wantAborted)
					}
					if len(summaries) != 1 || !reflect.DeepEqual(summaries[0], sum) {
						t.Errorf("callback=%+v, returned=%+v", summaries, sum)
					}
					switch {
					case event == "socket_failure_and_control_loss":
						if tc.stopMsg != nil {
							t.Errorf("Stop sent after the control connection was lost: %x", tc.stopMsg)
						}
					case tc.stopMsg == nil:
						t.Error("no Stop sent")
					case !isc:
						var stop wire.StopSessions
						if err := stop.Decode(tc.stopMsg); err != nil {
							t.Fatal(err)
						}
						if stop.Accept != wantAccept {
							t.Errorf("Stop-Sessions Accept=%v, want %v", stop.Accept, wantAccept)
						}
					}
				})
			})
		}
	}
}
