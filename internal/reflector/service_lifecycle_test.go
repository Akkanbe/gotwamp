package reflector_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

func TestLightCloseStopsServe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := &fakeConn{local: netip.MustParseAddrPort("127.0.0.1:862"), in: make(chan rxPacket), closed: make(chan struct{})}
		l, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{c.local}}, reflector.Deps{
			Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return c, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- l.Serve(ctx) }()
		synctest.Wait()
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Error("Close returned but Serve is still waiting for context cancellation")
			cancel()
			<-done
		}
	})
}

func TestLightLifecycleRaces(t *testing.T) {
	for _, before := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := &fakeConn{local: netip.MustParseAddrPort("127.0.0.1:862"), in: make(chan rxPacket), closed: make(chan struct{})}
			l, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{c.local}}, reflector.Deps{
				Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return c, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if before {
				if err := l.Close(); err != nil {
					t.Fatal(err)
				}
				if err := l.Serve(t.Context()); err == nil {
					t.Error("Serve after Close succeeded")
				}
				return
			}
			done := make(chan error, 1)
			go func() { done <- l.Serve(t.Context()) }()
			synctest.Wait()
			if err := l.Serve(t.Context()); err == nil {
				t.Error("second Serve succeeded")
			}
			var wg sync.WaitGroup
			for range 10 {
				wg.Go(func() {
					if err := l.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	// No ordering between registering Serve and Close is required.
	for range 100 {
		c := &fakeConn{local: netip.MustParseAddrPort("127.0.0.1:862"), in: make(chan rxPacket), closed: make(chan struct{})}
		l, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{c.local}}, reflector.Deps{
			Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return c, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- l.Serve(t.Context()) }()
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		<-done // Closed-before-start is a valid result of this race.
	}
}

func TestFullShutdownJoinsSession(t *testing.T) {
	for _, method := range []string{"close", "cancel"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			opened := make(chan *fakeConn, 1)
			f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}}, reflector.Deps{
				Listen: func(_ string, local netip.AddrPort, _ tstamp.Options) (tstamp.Conn, error) {
					c := &fakeConn{local: local, in: make(chan rxPacket), closed: make(chan struct{})}
					opened <- c
					return c, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			done := make(chan error, 1)
			go func() { done <- f.Serve(ctx) }()
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", f.Addrs()[0].String())
			if err != nil {
				t.Fatal(err)
			}
			c, err := client.Handshake(ctx, conn, client.Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Request(ctx, client.Request{SenderPort: 50000, Timeout: time.Hour}); err != nil {
				t.Fatal(err)
			}
			udp := <-opened
			if err := c.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.Sessions()) != 1 {
				t.Fatal("session not reported")
			}
			if err := f.Serve(ctx); err == nil {
				t.Error("second Serve succeeded")
			}
			// Exercise snapshots concurrently with the shutdown boundary.
			stopSnapshot := make(chan struct{})
			snapshotDone := make(chan struct{})
			go func() {
				defer close(snapshotDone)
				for {
					select {
					case <-stopSnapshot:
						return
					default:
						_ = f.Status()
						_ = f.Sessions()
					}
				}
			}()
			if method == "cancel" {
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				done <- nil
			} else if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			close(stopSnapshot)
			<-snapshotDone
			select {
			case <-udp.closed:
			default:
				t.Error("session socket remains open")
			}
			if st := f.Status(); st.Control.SessionsActive != 0 || st.Control.ConnectionsActive != 0 {
				t.Errorf("resources remain after Close: %+v", st)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFullListenRollbackAndValidation(t *testing.T) {
	failure := errors.New("injected bind failure")
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	cfg := reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0"), netip.MustParseAddrPort("127.0.0.2:0")}}
	deps := reflector.Deps{ListenControl: func(string, netip.AddrPort) (net.Listener, error) {
		calls++
		if calls == 1 {
			return ln, nil
		}
		return nil, failure
	}}
	if _, err := reflector.ListenFull(cfg, deps); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("listener not closed: %v", err)
	}
	calls = 0
	cfg.Timestamping = 255
	if _, err := reflector.ListenFull(cfg, deps); err == nil {
		t.Error("invalid timestamping accepted")
	}
	if calls != 0 {
		t.Errorf("validation opened %d listeners", calls)
	}
	cfg.Timestamping = 0
	cfg.ReadBuffer = -1
	if _, err := reflector.ListenFull(cfg, deps); err == nil {
		t.Error("invalid receive buffer accepted")
	}
	if calls != 0 {
		t.Errorf("validation opened %d listeners", calls)
	}
}

// serviceListener models a permanent accept failure or an idle listener.
type serviceListener struct {
	addr    net.TCPAddr
	failure error
	closed  chan struct{}
	once    sync.Once
}

func (l *serviceListener) Accept() (net.Conn, error) {
	if l.failure != nil {
		return nil, l.failure
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *serviceListener) Addr() net.Addr { return &l.addr }
func (l *serviceListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }

func TestFullListenerFailureStopsService(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("permanent accept failure")
		broken := &serviceListener{closed: make(chan struct{}), failure: failure}
		idle := &serviceListener{closed: make(chan struct{})}
		listeners := []net.Listener{broken, idle}
		next := 0
		f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0"), netip.MustParseAddrPort("127.0.0.2:0")}}, reflector.Deps{
			ListenControl: func(string, netip.AddrPort) (net.Listener, error) { l := listeners[next]; next++; return l, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()
		if err := f.Serve(ctx); !errors.Is(err, failure) {
			t.Fatalf("Serve: %v", err)
		}
		if ctx.Err() != nil {
			t.Error("Serve waited for caller cancellation after permanent accept failure")
		}
		for _, ln := range []*serviceListener{broken, idle} {
			select {
			case <-ln.closed:
			default:
				t.Error("listener not closed")
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// addressedConn gives an in-memory stream the IP endpoints TWAMP needs.
type addressedConn struct {
	net.Conn
	local, remote *net.TCPAddr
}

func (c *addressedConn) LocalAddr() net.Addr  { return c.local }
func (c *addressedConn) RemoteAddr() net.Addr { return c.remote }

type pipeListener struct {
	serviceListener
	conn net.Conn
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if l.conn != nil {
		c := l.conn
		l.conn = nil
		return c, nil
	}
	return l.serviceListener.Accept()
}

type delayedCloseConn struct {
	*fakeConn
	closing, release chan struct{}
	closeOnce        sync.Once
}

func (c *delayedCloseConn) Close() error {
	c.closeOnce.Do(func() { close(c.closing) })
	<-c.release
	return c.fakeConn.Close()
}

func TestFullCloseJoinsDrainingWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := net.Pipe()
		local := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 862}
		remote := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
		ln := &pipeListener{serviceListener: serviceListener{addr: *local, closed: make(chan struct{})}, conn: &addressedConn{Conn: a, local: local, remote: remote}}
		udp := &delayedCloseConn{fakeConn: &fakeConn{local: netip.MustParseAddrPort("127.0.0.1:50000"), in: make(chan rxPacket), closed: make(chan struct{})}, closing: make(chan struct{}), release: make(chan struct{})}
		f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{local.AddrPort()}}, reflector.Deps{
			ListenControl: func(string, netip.AddrPort) (net.Listener, error) { return ln, nil },
			Listen:        func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return udp, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		served := make(chan error, 1)
		go func() { served <- f.Serve(t.Context()) }()
		c, err := client.Handshake(t.Context(), &addressedConn{Conn: b, local: remote, remote: local}, client.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000, Timeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if err := c.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		<-udp.closing // Drain timer removed the session, but its UDP reader still runs.
		closed := make(chan error, 1)
		go func() { closed <- f.Close() }()
		synctest.Wait()
		returned := false
		select {
		case err := <-closed:
			returned = true
			t.Errorf("Close returned before draining worker finished: %v", err)
		default:
		}
		close(udp.release)
		if !returned {
			if err := <-closed; err != nil {
				t.Fatal(err)
			}
		}
		if err := <-served; err != nil {
			t.Fatal(err)
		}
	})
}
