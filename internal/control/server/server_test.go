package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// fakeSession records what the server does to a session.
type fakeSession struct {
	spec server.SessionSpec

	mu      sync.Mutex
	startAt time.Time
	stopAt  time.Time
	closed  int
	last    time.Time
}

func (s *fakeSession) Start(at time.Time) { s.mu.Lock(); s.startAt = at; s.mu.Unlock() }
func (s *fakeSession) Stop(until time.Time) {
	s.mu.Lock()
	s.stopAt = until
	s.mu.Unlock()
}

func (s *fakeSession) Close() error { s.mu.Lock(); s.closed++; s.mu.Unlock(); return nil }

func (s *fakeSession) Stats() server.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return server.SessionStats{Received: 3, Reflected: 2, LastReceived: s.last}
}

// touch records a test packet arriving now.
func (s *fakeSession) touch() { s.mu.Lock(); s.last = time.Now(); s.mu.Unlock() }

func (s *fakeSession) state() (start, stop time.Time, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startAt, s.stopAt, s.closed
}

// fakeHandler opens fakeSessions; inUse ports fail with EADDRINUSE.
type fakeHandler struct {
	mu       sync.Mutex
	inUse    map[uint16]bool
	err      error // returned for every port not in inUse, when set
	sessions []*fakeSession
	tried    []uint16
}

func (h *fakeHandler) Open(spec server.SessionSpec) (server.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tried = append(h.tried, spec.Local.Port())
	if h.inUse[spec.Local.Port()] {
		return nil, &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}
	}
	if h.err != nil {
		return nil, h.err
	}
	s := &fakeSession{spec: spec}
	h.sessions = append(h.sessions, s)
	return s, nil
}

func (h *fakeHandler) session(i int) *fakeSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[i]
}

// testRand makes the port scan start at Low (2-octet reads are zero) and
// gives other reads, such as the random part of SIDs, a counter.
type testRand struct {
	mu sync.Mutex
	n  byte
}

func (r *testRand) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(b)
	if len(b) != 2 {
		r.n++
		b[len(b)-1] = r.n
	}
	return len(b), nil
}

type addrConn struct {
	net.Conn
	local, remote net.Addr
}

func (c addrConn) LocalAddr() net.Addr  { return c.local }
func (c addrConn) RemoteAddr() net.Addr { return c.remote }

var (
	clientAddr = netip.MustParseAddrPort("192.0.2.1:40000")
	serverAddr = netip.MustParseAddrPort("198.51.100.2:862")
)

type harness struct {
	t *testing.T
	s *server.Server
	h *fakeHandler
}

func newHarness(t *testing.T, cfg server.Config) *harness {
	t.Helper()
	if cfg.Rand == nil {
		cfg.Rand = &testRand{}
	}
	h := &fakeHandler{inUse: map[uint16]bool{}}
	s, err := server.New(cfg, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, s: s, h: h}
}

// dial connects a raw client end to ServeConn from the given address.
func (hs *harness) dial(from netip.AddrPort) (net.Conn, <-chan struct{}) {
	return hs.dialTo(from, serverAddr)
}

// dialTo connects from one address to the server listening at another.
func (hs *harness) dialTo(from, to netip.AddrPort) (net.Conn, <-chan struct{}) {
	c, s := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hs.s.ServeConn(hs.t.Context(), addrConn{s, net.TCPAddrFromAddrPort(to), net.TCPAddrFromAddrPort(from)})
	}()
	return addrConn{c, net.TCPAddrFromAddrPort(from), net.TCPAddrFromAddrPort(to)}, done
}

// rawHandshakeOn completes the handshake on a connection opened by dialTo.
func (hs *harness) rawHandshakeOn(conn net.Conn) raw {
	hs.t.Helper()
	r := raw{hs.t, conn}
	r.recv(wire.ServerGreetingSize)
	r.send((&wire.SetUpResponse{Mode: wire.ModeUnauthenticated}).Append(nil))
	r.recv(wire.ServerStartSize)
	return r
}

// handshake connects with the client package.
func (hs *harness) handshake() (*client.Client, net.Conn, <-chan struct{}) {
	hs.t.Helper()
	conn, done := hs.dial(clientAddr)
	c, err := client.Handshake(hs.t.Context(), conn, client.Config{})
	if err != nil {
		hs.t.Fatal(err)
	}
	return c, conn, done
}

// raw drives the client side by hand, for messages the client package never sends.
type raw struct {
	t    *testing.T
	conn net.Conn
}

func (r raw) send(b []byte) {
	r.t.Helper()
	if _, err := r.conn.Write(b); err != nil {
		r.t.Fatalf("client write: %v", err)
	}
}

func (r raw) recv(n int) []byte {
	r.t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(r.conn, b); err != nil {
		r.t.Fatalf("client read: %v", err)
	}
	return b
}

// closed reports whether the server closed the connection.
func (r raw) closed() bool {
	_, err := r.conn.Read(make([]byte, 1))
	return errors.Is(err, io.EOF)
}

func (hs *harness) rawHandshake() (raw, <-chan struct{}) {
	hs.t.Helper()
	conn, done := hs.dial(clientAddr)
	r := raw{hs.t, conn}
	r.recv(wire.ServerGreetingSize)
	r.send((&wire.SetUpResponse{Mode: wire.ModeUnauthenticated}).Append(nil))
	var ss wire.ServerStart
	_ = ss.Decode(r.recv(wire.ServerStartSize))
	if ss.Accept != wire.AcceptOK {
		hs.t.Fatalf("server-start accept %v", ss.Accept)
	}
	return r, done
}

// request sends a Request-TW-Session and returns the Accept-Session.
func (r raw) request(req wire.RequestTWSession) wire.AcceptSession {
	r.t.Helper()
	r.send(req.Append(nil))
	var as wire.AcceptSession
	_ = as.Decode(r.recv(wire.AcceptSessionSize))
	return as
}

func validRequest() wire.RequestTWSession {
	return wire.RequestTWSession{IPVN: 4, SenderPort: 50000, ReceiverPort: 862, PaddingLength: 27, Timeout: ntptime.New(2, 0)}
}

func TestSessionLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		hs := newHarness(t, server.Config{StartTime: start.Add(-time.Hour)})
		c, _, done := hs.handshake()
		if c.ServerStartTime() != ntptime.FromTime(start.Add(-time.Hour)) {
			t.Errorf("Server-Start Start-Time = %v", c.ServerStartTime())
		}
		if g := c.Greeting(); g.Modes != wire.ModeUnauthenticated || g.Count != 2048 {
			t.Errorf("greeting = %+v", g)
		}
		ctx := t.Context()
		sess, err := c.Request(ctx, client.Request{SenderPort: 50000, ReceiverPort: 862, DSCP: 46, PaddingLength: 27, Timeout: 3 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if sess.Reflector != netip.MustParseAddrPort("198.51.100.2:862") || sess.AlternatePort || sess.SID.IsZero() {
			t.Errorf("session = %+v", sess)
		}
		fs := hs.h.session(0)
		want := server.SessionSpec{
			SID:           sess.SID,
			Local:         netip.MustParseAddrPort("198.51.100.2:862"),
			Sender:        netip.MustParseAddrPort("192.0.2.1:50000"),
			ControlPeer:   clientAddr,
			DSCP:          46,
			PaddingLength: 27,
			Timeout:       3 * time.Second,
			Security:      wire.SecurityUnauthenticated,
		}
		if fs.spec != want {
			t.Errorf("spec = %+v\nwant  %+v", fs.spec, want)
		}
		// SID = IPv4 address | NTP timestamp | random (RFC 4656 §3.5).
		if [4]byte(sess.SID[:4]) != [4]byte{198, 51, 100, 2} {
			t.Errorf("SID %v does not start with the server address", sess.SID)
		}
		infos := hs.s.Sessions()
		if len(infos) != 1 || infos[0].State != server.SessionAccepted || infos[0].Reflector != want.Local ||
			infos[0].Sender != want.Sender || infos[0].Stats.Received != 3 || !infos[0].AcceptedAt.Equal(start) {
			t.Errorf("Sessions() = %+v", infos)
		}

		time.Sleep(time.Second)
		if err := c.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if at, _, _ := fs.state(); !at.Equal(time.Now()) {
			t.Errorf("Start at %v, want now", at)
		}
		if st := hs.s.Sessions()[0].State; st != server.SessionActive {
			t.Errorf("state after start = %v", st)
		}
		time.Sleep(5 * time.Second)
		if err := c.Stop(ctx, wire.AcceptOK); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		_, stop, closed := fs.state()
		if !stop.Equal(time.Now().Add(3*time.Second)) || closed != 0 || hs.s.Sessions()[0].State != server.SessionDraining {
			t.Errorf("after stop: stop %v closed %d", stop, closed)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if _, _, closed := fs.state(); closed != 1 || len(hs.s.Sessions()) != 0 {
			t.Errorf("after drain: closed %d, sessions %d", closed, len(hs.s.Sessions()))
		}
		st := hs.s.Status()
		if st.ConnectionsActive != 1 || st.ConnectionsAccepted != 1 || st.SessionsActive != 0 {
			t.Errorf("status = %+v", st)
		}
		_ = c.Close()
		<-done
		if st := hs.s.Status(); st.ConnectionsActive != 0 {
			t.Errorf("connections after close = %d", st.ConnectionsActive)
		}
	})
}

func TestPortAllocation(t *testing.T) {
	tests := []struct {
		name      string
		cfg       server.Config
		inUse     []uint16
		err       error
		requested uint16
		accept    wire.Accept
		port      uint16
	}{
		{"requested port", server.Config{}, nil, nil, 862, wire.AcceptOK, 862},
		{"requested port in use", server.Config{}, []uint16{862}, nil, 862, wire.AcceptOK, 20000},
		{"zero asks for the range", server.Config{}, nil, nil, 0, wire.AcceptOK, 20000},
		{"scan skips ports in use", server.Config{}, []uint16{862, 20000, 20001}, nil, 862, wire.AcceptOK, 20002},
		{"requested port inside the range in use", server.Config{TestPorts: server.PortRange{Low: 30000, High: 30002}}, []uint16{30000}, nil, 30000, wire.AcceptOK, 30001},
		{"honor_requested_port false", server.Config{IgnoreRequestedPort: true}, nil, nil, 862, wire.AcceptOK, 20000},
		{"range exhausted", server.Config{TestPorts: server.PortRange{Low: 30000, High: 30001}}, []uint16{862, 30000, 30001}, nil, 862, wire.AcceptTemporaryLimit, 0},
		{"address not local", server.Config{}, nil, &net.OpError{Err: syscall.EADDRNOTAVAIL}, 862, wire.AcceptNotSupported, 0},
		{"handler says not local", server.Config{}, nil, server.ErrAddressNotLocal, 862, wire.AcceptNotSupported, 0},
		{"handler cannot serve the session", server.Config{}, nil, server.ErrUnsupportedRequest, 862, wire.AcceptNotSupported, 0},
		{"internal error", server.Config{}, nil, errors.New("boom"), 862, wire.AcceptInternalError, 0},
		{"port in use sentinel", server.Config{TestPorts: server.PortRange{Low: 1, High: 1}}, nil, server.ErrPortInUse, 862, wire.AcceptTemporaryLimit, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, tt.cfg)
				for _, p := range tt.inUse {
					hs.h.inUse[p] = true
				}
				hs.h.err = tt.err
				r, _ := hs.rawHandshake()
				req := validRequest()
				req.ReceiverPort = tt.requested
				as := r.request(req)
				if as.Accept != tt.accept || as.Port != tt.port {
					t.Errorf("Accept-Session = %+v, want accept %v port %d (tried %v)", as, tt.accept, tt.port, hs.h.tried)
				}
				if tt.accept != wire.AcceptOK && !as.SID.IsZero() {
					t.Errorf("rejected Accept-Session carries SID %v", as.SID)
				}
				// A rejected request keeps the connection usable.
				hs.h.err = nil
				clear(hs.h.inUse)
				if as := r.request(validRequest()); as.Accept != wire.AcceptOK {
					t.Errorf("next request: %+v", as)
				}
			})
		})
	}
}

func TestRequestAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{})
		r, _ := hs.rawHandshake()
		req := validRequest()
		req.SenderAddress = [16]byte{203, 0, 113, 5}
		req.ReceiverAddress = [16]byte{198, 51, 100, 7}
		req.StartTime = ntptime.FromTime(time.Now().Add(time.Minute))
		if as := r.request(req); as.Accept != wire.AcceptOK {
			t.Fatalf("Accept-Session = %+v", as)
		}
		// An IPv6 session needs an IPv6 control connection (SPEC §5.8).
		conn6, _ := hs.dialTo(netip.MustParseAddrPort("[2001:db8::1]:40000"), netip.MustParseAddrPort("[2001:db8::2]:862"))
		r6 := hs.rawHandshakeOn(conn6)
		req6 := validRequest()
		req6.IPVN = 6
		req6.SenderAddress = netip.MustParseAddr("2001:db8::5").As16()
		req6.ReceiverAddress = netip.MustParseAddr("2001:db8::7").As16()
		if as := r6.request(req6); as.Accept != wire.AcceptOK {
			t.Fatalf("Accept-Session = %+v", as)
		}
		s4, s6 := hs.h.session(0).spec, hs.h.session(1).spec
		if s4.Sender != netip.MustParseAddrPort("203.0.113.5:50000") || s4.Local != netip.MustParseAddrPort("198.51.100.7:862") ||
			!s4.StartTime.Equal(time.Now().Add(time.Minute)) {
			t.Errorf("IPv4 spec = %+v", s4)
		}
		if s6.Sender != netip.MustParseAddrPort("[2001:db8::5]:50000") || s6.Local != netip.MustParseAddrPort("[2001:db8::7]:862") {
			t.Errorf("IPv6 spec = %+v", s6)
		}
		if [4]byte(s6.SID[:4]) != [4]byte{0, 0, 0, 7} {
			t.Errorf("IPv6 SID = %v, want the last 4 octets of the address first", s6.SID)
		}
	})
}

func TestRequestValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(r *wire.RequestTWSession)
	}{
		{"conf-sender", func(r *wire.RequestTWSession) { r.ConfSender = 1 }},
		{"conf-receiver", func(r *wire.RequestTWSession) { r.ConfReceiver = 1 }},
		{"schedule slots", func(r *wire.RequestTWSession) { r.ScheduleSlots = 1 }},
		{"packets", func(r *wire.RequestTWSession) { r.Packets = 10 }},
		{"ipvn", func(r *wire.RequestTWSession) { r.IPVN = 5 }},
		{"type-p beyond dscp", func(r *wire.RequestTWSession) { r.TypeP = 0x40000000 }},
		{"padding over max", func(r *wire.RequestTWSession) { r.PaddingLength = 9001 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, server.Config{})
				r, _ := hs.rawHandshake()
				req := validRequest()
				tt.mutate(&req)
				if as := r.request(req); as.Accept != wire.AcceptNotSupported || as.Port != 0 {
					t.Errorf("Accept-Session = %+v, want accept 3 port 0", as)
				}
				if len(hs.h.tried) != 0 {
					t.Errorf("handler opened ports %v for an invalid request", hs.h.tried)
				}
			})
		})
	}
}

func TestSessionLimits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{MaxSessionsPerConnection: 2, MaxSessions: 3})
		r1, _ := hs.rawHandshake()
		for i, want := range []wire.Accept{wire.AcceptOK, wire.AcceptOK, wire.AcceptPermanentLimit} {
			if as := r1.request(validRequest()); as.Accept != want {
				t.Errorf("connection 1 request %d: accept %v, want %v", i, as.Accept, want)
			}
		}
		r2, _ := hs.rawHandshake()
		for i, want := range []wire.Accept{wire.AcceptOK, wire.AcceptTemporaryLimit} {
			if as := r2.request(validRequest()); as.Accept != want {
				t.Errorf("connection 2 request %d: accept %v, want %v", i, as.Accept, want)
			}
		}
		if st := hs.s.Status(); st.SessionsActive != 3 || st.ConnectionsActive != 2 {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestConnectionRefused(t *testing.T) {
	t.Run("connection limit", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, server.Config{MaxConnections: 1})
			c1, _, _ := hs.handshake()
			conn, done := hs.dial(clientAddr)
			if _, err := client.Handshake(t.Context(), conn, client.Config{}); !errors.Is(err, client.ErrServerRefused) {
				t.Errorf("second connection: %v", err)
			}
			<-done
			if st := hs.s.Status(); st.ConnectionsAccepted != 1 || st.ConnectionsRejected != 1 || st.ConnectionsActive != 1 {
				t.Errorf("status = %+v", st)
			}
			_ = c1.Close()
		})
	})
	t.Run("acl", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, server.Config{ACL: []netip.Prefix{netip.MustParsePrefix("::ffff:192.0.2.0/120")}})
			c, _, _ := hs.handshake() // 192.0.2.1 is allowed
			conn, done := hs.dial(netip.MustParseAddrPort("203.0.113.1:1"))
			if _, err := client.Handshake(t.Context(), conn, client.Config{}); !errors.Is(err, client.ErrServerRefused) {
				t.Errorf("connection outside the ACL: %v", err)
			}
			<-done
			_ = c.Close()
		})
	})
}

func TestSetupModes(t *testing.T) {
	tests := []struct {
		name   string
		mode   wire.Modes
		accept wire.Accept // 255: no Server-Start at all
	}{
		{"mode 0 closes", 0, 255},
		{"authenticated not offered", wire.ModeAuthenticated, wire.AcceptFailure},
		{"two security modes", wire.ModeUnauthenticated | wire.ModeAuthenticated, wire.AcceptFailure},
		{"feature bit not offered", wire.ModeUnauthenticated | wire.ModeReflectOctets, wire.AcceptFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, server.Config{})
				conn, done := hs.dial(clientAddr)
				r := raw{t, conn}
				r.recv(wire.ServerGreetingSize)
				r.send((&wire.SetUpResponse{Mode: tt.mode}).Append(nil))
				if tt.accept != 255 {
					var ss wire.ServerStart
					_ = ss.Decode(r.recv(wire.ServerStartSize))
					if ss.Accept != tt.accept {
						t.Errorf("Server-Start accept %v, want %v", ss.Accept, tt.accept)
					}
				}
				if !r.closed() {
					t.Error("connection left open")
				}
				<-done
			})
		})
	}
}

func TestCommandHandling(t *testing.T) {
	cmd := func(c wire.Command) []byte {
		b := make([]byte, 32)
		b[0] = byte(c)
		return b
	}
	stop := func(n uint32) []byte { return (&wire.StopSessions{Sessions: n}).Append(nil) }
	tests := []struct {
		name   string
		run    func(r raw)
		closed bool
	}{
		{"unknown command", func(r raw) {
			r.send(cmd(wire.CommandFetchSession)[:16])
			var as wire.AcceptSession
			_ = as.Decode(r.recv(wire.AcceptSessionSize))
			if as.Accept != wire.AcceptNotSupported {
				r.t.Errorf("Accept-Session accept %v", as.Accept)
			}
		}, true},
		{"forbidden command", func(r raw) {
			r.send(cmd(wire.CommandForbidden)[:16])
			r.recv(wire.AcceptSessionSize)
		}, true},
		{"start without sessions", func(r raw) {
			r.send(cmd(wire.CommandStartSessions))
			var ack wire.StartAck
			_ = ack.Decode(r.recv(wire.StartAckSize))
			if ack.Accept != wire.AcceptFailure {
				r.t.Errorf("Start-Ack accept %v", ack.Accept)
			}
			if as := r.request(validRequest()); as.Accept != wire.AcceptOK {
				r.t.Errorf("request after failed start: %+v", as)
			}
		}, false},
		{"stop of nothing is ignored", func(r raw) {
			r.send(stop(0))
			if as := r.request(validRequest()); as.Accept != wire.AcceptOK {
				r.t.Errorf("request after stop 0: %+v", as)
			}
		}, false},
		{"stop with sessions outside active closes", func(r raw) { r.send(stop(1)) }, true},
		{"request while active closes", func(r raw) {
			r.request(validRequest())
			r.send(cmd(wire.CommandStartSessions))
			r.recv(wire.StartAckSize)
			r.send(appendRequest(validRequest()))
		}, true},
		{"start while active closes", func(r raw) {
			r.request(validRequest())
			r.send(cmd(wire.CommandStartSessions))
			r.recv(wire.StartAckSize)
			r.send(cmd(wire.CommandStartSessions))
		}, true},
		{"stop count mismatch closes", func(r raw) {
			r.request(validRequest())
			r.request(validRequest())
			r.send(cmd(wire.CommandStartSessions))
			r.recv(wire.StartAckSize)
			r.send(stop(1))
		}, true},
		{"new run after stop", func(r raw) {
			r.request(validRequest())
			r.send(cmd(wire.CommandStartSessions))
			r.recv(wire.StartAckSize)
			r.send(stop(1))
			if as := r.request(validRequest()); as.Accept != wire.AcceptOK {
				r.t.Errorf("request after stop: %+v", as)
			}
			r.send(cmd(wire.CommandStartSessions))
			var ack wire.StartAck
			_ = ack.Decode(r.recv(wire.StartAckSize))
			if ack.Accept != wire.AcceptOK {
				r.t.Errorf("second start accept %v", ack.Accept)
			}
			r.send(stop(1)) // only the new session is started
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, server.Config{})
				r, done := hs.rawHandshake()
				tt.run(r)
				if tt.closed {
					if !r.closed() {
						t.Error("connection left open")
					}
					<-done
					return
				}
				synctest.Wait()
				select {
				case <-done:
					t.Error("connection closed")
				default:
				}
			})
		})
	}
}

func TestServWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{ServWait: 10 * time.Second})
		r, done := hs.rawHandshake()
		time.Sleep(9 * time.Second)
		r.request(validRequest()) // activity restarts SERVWAIT
		time.Sleep(9 * time.Second)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("closed before SERVWAIT")
		default:
		}
		time.Sleep(time.Second)
		<-done
		if _, _, closed := hs.h.session(0).state(); closed != 1 {
			t.Errorf("accepted session closed %d times, want 1", closed)
		}
	})
}

func TestServWaitBeforeSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{ServWait: 5 * time.Second})
		conn, done := hs.dial(clientAddr)
		raw{t, conn}.recv(wire.ServerGreetingSize)
		start := time.Now()
		<-done
		if d := time.Since(start); d != 5*time.Second {
			t.Errorf("closed after %v", d)
		}
	})
}

func TestServWaitSuspendedWhileActive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{ServWait: 10 * time.Second, RefWait: 30 * time.Second})
		c, _, done := hs.handshake()
		if _, err := c.Request(t.Context(), client.Request{SenderPort: 1}); err != nil {
			t.Fatal(err)
		}
		if err := c.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		fs := hs.h.session(0)
		for range 5 {
			time.Sleep(20 * time.Second)
			fs.touch() // packets keep REFWAIT from expiring
		}
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("SERVWAIT closed an active connection")
		default:
		}
		if _, _, closed := fs.state(); closed != 0 {
			t.Fatal("REFWAIT released a session that receives packets")
		}
		if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		<-done
	})
}

func TestRefWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{RefWait: 30 * time.Second, ServWait: time.Hour})
		r, done := hs.rawHandshake()
		r.request(validRequest())
		r.request(validRequest())
		r.send((&wire.StartSessions{}).Append(nil))
		r.recv(wire.StartAckSize)
		quiet, busy := hs.h.session(0), hs.h.session(1)
		time.Sleep(20 * time.Second)
		busy.touch()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if _, _, closed := quiet.state(); closed != 1 {
			t.Errorf("quiet session closed %d times after REFWAIT, want 1", closed)
		}
		if _, _, closed := busy.state(); closed != 0 {
			t.Error("busy session released")
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if _, _, closed := busy.state(); closed != 1 {
			t.Error("busy session not released after its own REFWAIT")
		}
		// Every session timed out: the connection is back to REQUEST_WAIT.
		if as := r.request(validRequest()); as.Accept != wire.AcceptOK {
			t.Errorf("request after REFWAIT: %+v", as)
		}
		select {
		case <-done:
			t.Error("connection closed")
		default:
		}
	})
}

func TestPartialMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{PartialMessageTimeout: 7 * time.Second})
		r, done := hs.rawHandshake()
		r.send(appendRequest(validRequest())[:50])
		start := time.Now()
		<-done
		if d := time.Since(start); d != 7*time.Second {
			t.Errorf("closed after %v", d)
		}
	})
}

func TestDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{})
		r, done := hs.rawHandshake()
		req := validRequest()
		req.Timeout = ntptime.New(5, 0)
		r.request(req)
		r.send((&wire.StartSessions{}).Append(nil))
		r.recv(wire.StartAckSize)
		_ = r.conn.Close()
		<-done
		started := hs.h.session(0)
		_, stop, closed := started.state()
		if !stop.Equal(time.Now().Add(5*time.Second)) || closed != 0 {
			t.Errorf("started session: stop %v closed %d", stop, closed)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if _, _, closed := started.state(); closed != 1 || len(hs.s.Sessions()) != 0 {
			t.Errorf("started session closed %d times after Timeout", closed)
		}
	})
}

func TestDisconnectReleasesAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{})
		r, done := hs.rawHandshake()
		r.request(validRequest())
		_ = r.conn.Close()
		<-done
		if _, _, closed := hs.h.session(0).state(); closed != 1 {
			t.Errorf("accepted session closed %d times, want 1", closed)
		}
	})
}

// TestServe runs the server on a real TCP listener and shuts it down with
// sessions still draining.
func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	hs := newHarness(t, server.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error)
	go func() { served <- hs.s.Serve(ctx, ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Handshake(t.Context(), conn, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := c.Request(t.Context(), client.Request{SenderPort: 50000, Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Reflector.Addr() != netip.MustParseAddr("127.0.0.1") || hs.h.session(0).spec.Sender.Addr() != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("session %+v spec %+v", sess, hs.h.session(0).spec)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-served; err != nil {
		t.Errorf("Serve: %v", err)
	}
	if _, _, closed := hs.h.session(0).state(); closed != 1 {
		t.Errorf("draining session closed %d times at shutdown, want 1", closed)
	}
	if st := hs.s.Status(); st.ConnectionsActive != 0 || st.SessionsActive != 0 {
		t.Errorf("status after shutdown = %+v", st)
	}
	_ = c.Close()
}

func TestServeListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	_ = ln.Close()
	hs := newHarness(t, server.Config{})
	if err := hs.s.Serve(t.Context(), ln); err == nil {
		t.Error("Serve on a closed listener succeeded")
	}
}

func TestConfig(t *testing.T) {
	bad := []server.Config{
		{ACL: []netip.Prefix{{}}},
		{MaxConnections: -1},
		{ServWait: -1},
		{TestPorts: server.PortRange{Low: 30000, High: 20000}},
		{Count: 1000},
		{Count: 3072},
	}
	for _, c := range bad {
		if _, err := server.New(c, &fakeHandler{}, nil); err == nil {
			t.Errorf("New(%+v) succeeded", c)
		}
	}
	if _, err := server.New(server.Config{}, nil, nil); err == nil {
		t.Error("New with a nil handler succeeded")
	}
	d := server.Config{}.WithDefaults()
	if d.MaxConnections != 64 || d.MaxSessionsPerConnection != 16 || d.MaxSessions != 1024 || d.ServWait != 900*time.Second ||
		d.RefWait != 900*time.Second || d.PartialMessageTimeout != 60*time.Second || d.TestPorts != (server.PortRange{Low: 20000, High: 29999}) ||
		d.MaxPadding != 9000 || d.Count != 2048 || d.Rand == nil || d.Now == nil {
		t.Errorf("defaults = %+v", d)
	}
}

func TestSessionStateString(t *testing.T) {
	var got []string
	for _, s := range []server.SessionState{server.SessionAccepted, server.SessionActive, server.SessionDraining, 0} {
		got = append(got, s.String())
	}
	if !slices.Equal(got, []string{"accepted", "active", "draining", "unknown"}) {
		t.Errorf("names = %v", got)
	}
}

func appendRequest(r wire.RequestTWSession) []byte { return r.Append(nil) }

// TestHugeTimeoutIsCappedAtRefWait checks that a client cannot hold a
// session past REFWAIT by asking for a huge Timeout, neither by stopping it
// nor by disconnecting (xreview C1).
func TestHugeTimeoutIsCappedAtRefWait(t *testing.T) {
	for _, stop := range []bool{true, false} {
		name := "disconnect"
		if stop {
			name = "stop-sessions"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, server.Config{RefWait: 30 * time.Second, ServWait: time.Hour})
				r, done := hs.rawHandshake()
				req := validRequest()
				req.Timeout = ntptime.FromDuration(100 * 365 * 24 * time.Hour)
				r.request(req)
				r.send((&wire.StartSessions{}).Append(nil))
				r.recv(wire.StartAckSize)
				if stop {
					r.send((&wire.StopSessions{Sessions: 1}).Append(nil))
				} else {
					_ = r.conn.Close()
					<-done
				}
				synctest.Wait()
				if _, until, _ := hs.h.session(0).state(); !until.Equal(time.Now().Add(30 * time.Second)) {
					t.Errorf("reflecting until %v, want REFWAIT from now", until)
				}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				if _, _, closed := hs.h.session(0).state(); closed != 1 || hs.s.Status().SessionsActive != 0 {
					t.Errorf("session closed %d times, %d active after REFWAIT", closed, hs.s.Status().SessionsActive)
				}
			})
		})
	}
}

func TestPrivilegedRequestedPort(t *testing.T) {
	tests := []struct {
		requested, want uint16
	}{
		{123, 20000},  // privileged: an alternate port instead
		{1023, 20000}, // privileged
		{862, 862},    // TWAMP's own port is honored
		{1024, 1024},
		{50000, 50000},
	}
	for _, tt := range tests {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, server.Config{})
			r, _ := hs.rawHandshake()
			req := validRequest()
			req.ReceiverPort = tt.requested
			if as := r.request(req); as.Accept != wire.AcceptOK || as.Port != tt.want {
				t.Errorf("requested %d: Accept-Session %+v, want port %d", tt.requested, as, tt.want)
			}
		})
	}
	synctest.Test(t, func(t *testing.T) {
		// A privileged port inside the configured test range is honored.
		hs := newHarness(t, server.Config{TestPorts: server.PortRange{Low: 800, High: 900}})
		r, _ := hs.rawHandshake()
		req := validRequest()
		req.ReceiverPort = 850
		if as := r.request(req); as.Port != 850 {
			t.Errorf("Accept-Session %+v, want 850", as)
		}
	})
}

// TestInjectedClockDoesNotShiftDeadlines checks that SERVWAIT follows the
// real clock even when Config.Now is far from it (xreview m4).
func TestInjectedClockDoesNotShiftDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{
			ServWait: 5 * time.Second,
			Now:      func() time.Time { return time.Now().Add(-100 * 24 * time.Hour) },
		})
		r, done := hs.rawHandshake()
		_ = r
		start := time.Now()
		<-done
		if d := time.Since(start); d != 5*time.Second {
			t.Errorf("SERVWAIT closed after %v, want 5s", d)
		}
	})
}

// flakyListener fails Accept with err the first n times.
type flakyListener struct {
	net.Listener
	mu  sync.Mutex
	n   int
	err error
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if l.n > 0 {
		l.n--
		l.mu.Unlock()
		return nil, l.err
	}
	l.mu.Unlock()
	return l.Listener.Accept()
}

// TestServeRetriesTemporaryAcceptErrors checks that a temporary Accept error
// does not end Serve, and that a permanent one on one listener leaves the
// other serving (xreview M1, m6).
func TestServeRetriesTemporaryAcceptErrors(t *testing.T) {
	listen := func() net.Listener {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skipf("cannot listen: %v", err)
		}
		return ln
	}
	emfile := &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	flaky := &flakyListener{Listener: listen(), n: 3, err: emfile}
	broken := &flakyListener{Listener: listen(), n: 1, err: errors.New("permanent failure")}
	hs := newHarness(t, server.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- hs.s.Serve(ctx, flaky, broken) }()

	conn, err := net.Dial("tcp", flaky.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Handshake(t.Context(), conn, client.Config{})
	if err != nil {
		t.Fatalf("handshake after temporary accept errors: %v", err)
	}
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		t.Fatalf("Serve ended while a listener still serves: %v", err)
	default:
	}
	cancel()
	err = <-served
	if err == nil || !strings.Contains(err.Error(), "permanent failure") {
		t.Errorf("Serve = %v, want the permanent failure of the broken listener", err)
	}
	if st := hs.s.Status(); st.SessionsActive != 0 || st.ConnectionsActive != 0 {
		t.Errorf("status after Serve = %+v", st)
	}
	_ = c.Close()
}

// TestServeConcurrentCalls checks that two Serve calls sharing a server end
// cleanly, the last one releasing the sessions (xreview m6).
func TestServeConcurrentCalls(t *testing.T) {
	ln1, err1 := net.Listen("tcp", "127.0.0.1:0")
	ln2, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err1 != nil || err2 != nil {
		t.Skip("cannot listen")
	}
	hs := newHarness(t, server.Config{})
	ctx1, cancel1 := context.WithCancel(t.Context())
	ctx2, cancel2 := context.WithCancel(t.Context())
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() { done1 <- hs.s.Serve(ctx1, ln1) }()
	go func() { done2 <- hs.s.Serve(ctx2, ln2) }()

	conn, err := net.Dial("tcp", ln2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Handshake(t.Context(), conn, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); err != nil {
		t.Fatal(err)
	}
	cancel1()
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	// The first call ending leaves the second call's session alone.
	if n := hs.s.Status().SessionsActive; n != 1 {
		t.Errorf("sessions after the first Serve ended = %d, want 1", n)
	}
	cancel2()
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if st := hs.s.Status(); st.SessionsActive != 0 || st.ConnectionsActive != 0 {
		t.Errorf("status after both = %+v", st)
	}
	_ = c.Close()
}

func TestServeNeedsAListener(t *testing.T) {
	hs := newHarness(t, server.Config{})
	if err := hs.s.Serve(t.Context()); err == nil {
		t.Error("Serve without listeners succeeded")
	}
}

// TestRequestFamily checks that a session takes the address family of its
// control connection (SPEC §5.8, audit W7).
func TestRequestFamily(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		ipvn     uint8
		accept   wire.Accept
	}{
		{"IPv4 over IPv4", "192.0.2.1:40000", "198.51.100.2:862", wire.IPVN4, wire.AcceptOK},
		{"IPv6 over IPv4", "192.0.2.1:40000", "198.51.100.2:862", wire.IPVN6, wire.AcceptNotSupported},
		{"IPv4 over IPv6", "[2001:db8::1]:40000", "[2001:db8::2]:862", wire.IPVN4, wire.AcceptNotSupported},
		{"IPv6 over IPv6", "[2001:db8::1]:40000", "[2001:db8::2]:862", wire.IPVN6, wire.AcceptOK},
		{"IPv4 over IPv4-mapped", "[::ffff:192.0.2.1]:40000", "[::ffff:198.51.100.2]:862", wire.IPVN4, wire.AcceptOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, server.Config{})
				conn, _ := hs.dialTo(netip.MustParseAddrPort(tt.from), netip.MustParseAddrPort(tt.to))
				r := hs.rawHandshakeOn(conn)
				req := validRequest()
				req.IPVN = tt.ipvn
				if as := r.request(req); as.Accept != tt.accept {
					t.Errorf("Accept-Session = %+v, want accept %v", as, tt.accept)
				}
			})
		})
	}
}

// TestIPv4MappedControlConnection runs the server on a dual-stack [::]
// listener and connects over IPv4: the control connection then has
// IPv4-mapped addresses, which must count as IPv4 for IPVN, the bind address
// and the SID (SPEC §11.3, audit W12).
func TestIPv4MappedControlConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Skipf("no dual-stack listener: %v", err)
	}
	hs := newHarness(t, server.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error)
	go func() { served <- hs.s.Serve(ctx, ln) }()
	defer func() { cancel(); <-served }()

	port := ln.Addr().(*net.TCPAddr).Port
	conn, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("no IPv4 to a dual-stack listener: %v", err)
	}
	c, err := client.Handshake(t.Context(), conn, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sess, err := c.Request(t.Context(), client.Request{SenderPort: 50000})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Request.IPVN != wire.IPVN4 {
		t.Errorf("client sent IPVN %d", sess.Request.IPVN)
	}
	spec := hs.h.session(0).spec
	lo := netip.MustParseAddr("127.0.0.1")
	if spec.Local.Addr() != lo || spec.Sender.Addr() != lo || spec.ControlPeer.Addr() != lo || [4]byte(spec.SID[:4]) != [4]byte{127, 0, 0, 1} {
		t.Errorf("session spec %+v: want plain IPv4 addresses", spec)
	}
}

// syncBuffer is a bytes.Buffer safe for a logger and a test reading it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestConnectionCloseLogged checks that the operator sees, at the default
// level, why the server closed a control connection.
func TestConnectionCloseLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var out syncBuffer
		hs := newHarness(t, server.Config{})
		s, err := server.New(server.Config{Rand: &testRand{}}, hs.h, slog.New(slog.NewTextHandler(&out, nil)))
		if err != nil {
			t.Fatal(err)
		}
		hs.s = s
		r, done := hs.rawHandshake()
		r.send((&wire.StopSessions{Sessions: 3}).Append(nil)) // nothing was started
		<-done
		log := out.String()
		if !strings.Contains(log, "level=INFO") || !strings.Contains(log, `msg="control server: connection closed"`) ||
			!strings.Contains(log, "protocol violation") {
			t.Errorf("log:\n%s", log)
		}
	})
}
