package client_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// addrConn gives one end of a net.Pipe TCP addresses.
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

// pipe returns the client end, with TCP addresses, and the server end.
func pipe(local, remote netip.AddrPort) (net.Conn, net.Conn) {
	c, s := net.Pipe()
	return addrConn{c, net.TCPAddrFromAddrPort(local), net.TCPAddrFromAddrPort(remote)}, s
}

// script plays the server side of the control protocol.
type script struct {
	t    *testing.T
	conn net.Conn
}

func (s script) send(m interface{ Append([]byte) []byte }) {
	s.t.Helper()
	if _, err := s.conn.Write(m.Append(nil)); err != nil {
		s.t.Errorf("server write: %v", err)
	}
}

func (s script) recv(n int) []byte {
	s.t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(s.conn, b); err != nil {
		s.t.Errorf("server read: %v", err)
	}
	return b
}

// handshake plays Greeting, reads Set-Up-Response and answers Server-Start.
func (s script) handshake() []byte {
	s.send(&wire.ServerGreeting{Modes: wire.ModeUnauthenticated, Count: 2048})
	setup := s.recv(wire.SetUpResponseSize)
	s.send(&wire.ServerStart{StartTime: ntptime.New(100, 0)})
	return setup
}

// serve runs fn as the server in a goroutine and returns a wait function.
func serve(t *testing.T, conn net.Conn, fn func(s script)) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(script{t, conn})
	}()
	return func() { <-done }
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "golden", name)) //nolint:gosec // names are constants of this test
	if err != nil {
		t.Fatal(err)
	}
	var digits strings.Builder
	for line := range strings.Lines(string(raw)) {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		digits.WriteString(strings.Join(strings.Fields(line), ""))
	}
	b, err := hex.DecodeString(digits.String())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFullExchange runs the whole client side against a scripted server and
// checks the octets it sends against the golden files.
func TestFullExchange(t *testing.T) {
	cc, sc := pipe(clientAddr, serverAddr)
	sid := wire.SID{1, 2, 3, 4}
	var gotRequest, gotStart, gotStop []byte
	wait := serve(t, sc, func(s script) {
		setup := s.handshake()
		if !bytes.Equal(setup, (&wire.SetUpResponse{Mode: wire.ModeUnauthenticated}).Append(nil)) {
			t.Errorf("Set-Up-Response = %x", setup)
		}
		gotRequest = s.recv(wire.RequestTWSessionSize)
		s.send(&wire.AcceptSession{Port: 20000, SID: sid})
		gotStart = s.recv(wire.StartSessionsSize)
		s.send(&wire.StartAck{})
		gotStop = s.recv(wire.StopSessionsSize)
		_ = s.conn.Close()
	})

	ctx := t.Context()
	c, err := client.Handshake(ctx, cc, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if c.State() != client.StateIdle || c.Greeting().Count != 2048 || c.ServerStartTime() != ntptime.New(100, 0) {
		t.Errorf("after handshake: state %v greeting %+v start %v", c.State(), c.Greeting(), c.ServerStartTime())
	}
	sess, err := c.Request(ctx, client.Request{SenderPort: 49152, PaddingLength: 27, DSCP: 46})
	if err != nil {
		t.Fatal(err)
	}
	if sess.SID != sid || sess.Reflector != netip.MustParseAddrPort("198.51.100.2:20000") || !sess.AlternatePort {
		t.Errorf("session = %+v", sess)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if c.State() != client.StateActive || len(c.Started()) != 1 || c.Err() != nil {
		t.Errorf("after start: state %v started %d err %v", c.State(), len(c.Started()), c.Err())
	}
	if err := c.Stop(ctx, wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	if c.State() != client.StateIdle {
		t.Errorf("after stop: state %v", c.State())
	}
	wait()
	_ = c.Close()

	for _, g := range []struct {
		name string
		got  []byte
	}{
		{"request-tw-session-ipv4.hex", gotRequest},
		{"start-sessions.hex", gotStart},
		{"stop-sessions.hex", gotStop},
	} {
		if want := readGolden(t, g.name); !bytes.Equal(g.got, want) {
			t.Errorf("%s\n got  %x\n want %x", g.name, g.got, want)
		}
	}
}

// TestReuseAfterStop requests two sessions, runs them, and runs another
// session on the same connection afterwards (SPEC §13).
func TestReuseAfterStop(t *testing.T) {
	cc, sc := pipe(clientAddr, serverAddr)
	wait := serve(t, sc, func(s script) {
		s.handshake()
		for range 2 {
			r := s.recv(wire.RequestTWSessionSize)
			var req wire.RequestTWSession
			_ = req.Decode(r)
			s.send(&wire.AcceptSession{Port: req.ReceiverPort})
		}
		s.recv(wire.StartSessionsSize)
		s.send(&wire.StartAck{})
		var stop wire.StopSessions
		_ = stop.Decode(s.recv(wire.StopSessionsSize))
		if stop.Sessions != 2 || stop.Accept != wire.AcceptFailure {
			t.Errorf("first stop = %+v", stop)
		}
		s.recv(wire.RequestTWSessionSize)
		s.send(&wire.AcceptSession{Port: 5000})
		s.recv(wire.StartSessionsSize)
		s.send(&wire.StartAck{})
		_ = stop.Decode(s.recv(wire.StopSessionsSize))
		if stop.Sessions != 1 {
			t.Errorf("second stop = %+v", stop)
		}
	})
	ctx := t.Context()
	c, err := client.Handshake(ctx, cc, client.Config{RejectAlternatePort: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{6000, 6001} {
		if s, err := c.Request(ctx, client.Request{SenderPort: port}); err != nil || s.AlternatePort {
			t.Fatalf("request %d: %+v, %v", port, s, err)
		}
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, wire.AcceptFailure); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(ctx, client.Request{SenderPort: 5000}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	wait()
	_ = c.Close()
}

func TestRequestFields(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 500_000_000, time.UTC)
	tests := []struct {
		name          string
		local, remote netip.AddrPort
		req           client.Request
		check         func(t *testing.T, r wire.RequestTWSession, s *client.Session)
	}{
		{
			"defaults", clientAddr, serverAddr,
			client.Request{SenderPort: 50000},
			func(t *testing.T, r wire.RequestTWSession, s *client.Session) {
				if r.IPVN != 4 || r.ReceiverPort != 50000 || r.Timeout != ntptime.New(2, 0) || !r.StartTime.IsZero() || r.TypeP != 0 || r.PaddingLength != 0 {
					t.Errorf("request = %+v", r)
				}
				if s.Reflector.Addr() != serverAddr.Addr() {
					t.Errorf("reflector = %v", s.Reflector)
				}
			},
		},
		{
			"overrides", clientAddr, serverAddr,
			client.Request{SenderPort: 50000, ReceiverPort: 862, Timeout: 1500 * time.Millisecond, StartTime: start, DSCP: 10, PaddingLength: 100},
			func(t *testing.T, r wire.RequestTWSession, _ *client.Session) {
				if r.ReceiverPort != 862 || r.Timeout != ntptime.New(1, 1<<31) || r.StartTime != ntptime.FromTime(start) || r.TypeP != wire.TypePFromDSCP(10) || r.PaddingLength != 100 {
					t.Errorf("request = %+v", r)
				}
			},
		},
		{
			"zero addresses", clientAddr, serverAddr,
			client.Request{SenderPort: 1, ZeroAddresses: true},
			func(t *testing.T, r wire.RequestTWSession, s *client.Session) {
				if r.SenderAddress != [16]byte{} || r.ReceiverAddress != [16]byte{} || r.IPVN != 4 || s.Reflector.Addr() != serverAddr.Addr() {
					t.Errorf("request = %+v", r)
				}
			},
		},
		{
			"ipv6", netip.MustParseAddrPort("[2001:db8::1]:40000"), netip.MustParseAddrPort("[2001:db8::2]:862"),
			client.Request{SenderPort: 1},
			func(t *testing.T, r wire.RequestTWSession, _ *client.Session) {
				if r.IPVN != 6 || r.SenderAddress != netip.MustParseAddr("2001:db8::1").As16() || r.ReceiverAddress != netip.MustParseAddr("2001:db8::2").As16() {
					t.Errorf("request = %+v", r)
				}
			},
		},
		{
			"ipv4-mapped control connection", netip.MustParseAddrPort("[::ffff:192.0.2.1]:40000"), netip.MustParseAddrPort("[::ffff:198.51.100.2]:862"),
			client.Request{SenderPort: 1},
			func(t *testing.T, r wire.RequestTWSession, _ *client.Session) {
				if r.IPVN != 4 || r.SenderAddress != [16]byte{192, 0, 2, 1} || r.ReceiverAddress != [16]byte{198, 51, 100, 2} {
					t.Errorf("request = %+v", r)
				}
			},
		},
		{
			"explicit receiver", clientAddr, serverAddr,
			client.Request{SenderPort: 1, ReceiverAddress: netip.MustParseAddr("203.0.113.9")},
			func(t *testing.T, r wire.RequestTWSession, s *client.Session) {
				if r.ReceiverAddress != [16]byte{203, 0, 113, 9} || s.Reflector.Addr() != netip.MustParseAddr("203.0.113.9") {
					t.Errorf("request = %+v, reflector %v", r, s.Reflector)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, sc := pipe(tt.local, tt.remote)
			var req wire.RequestTWSession
			wait := serve(t, sc, func(s script) {
				s.handshake()
				_ = req.Decode(s.recv(wire.RequestTWSessionSize))
				s.send(&wire.AcceptSession{Port: req.ReceiverPort})
			})
			c, err := client.Handshake(t.Context(), cc, client.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := c.Request(t.Context(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			wait()
			tt.check(t, req, sess)
			if sess.Request != req {
				t.Errorf("Session.Request = %+v, sent %+v", sess.Request, req)
			}
			_ = c.Close()
		})
	}
}

func TestRequestInvalid(t *testing.T) {
	tests := []struct {
		name   string
		remote net.Addr
		req    client.Request
	}{
		{"dscp out of range", net.TCPAddrFromAddrPort(serverAddr), client.Request{DSCP: 64}},
		{"mixed families", net.TCPAddrFromAddrPort(serverAddr), client.Request{SenderAddress: netip.MustParseAddr("2001:db8::1")}},
		{"no ip address", pipeAddr{}, client.Request{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c0, sc := net.Pipe()
			cc := addrConn{c0, net.TCPAddrFromAddrPort(clientAddr), tt.remote}
			wait := serve(t, sc, func(s script) { s.handshake() })
			c, err := client.Handshake(t.Context(), cc, client.Config{})
			if err != nil {
				t.Fatal(err)
			}
			wait()
			if _, err := c.Request(t.Context(), tt.req); stageOf(err) != client.StageRequest {
				t.Errorf("err = %v", err)
			}
			_ = c.Close()
		})
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func stageOf(err error) client.Stage {
	var ce *client.Error
	if errors.As(err, &ce) {
		return ce.Stage
	}
	return 0
}

// TestFailures checks that each failure names its stage and cause, and that
// the connection is closed.
func TestFailures(t *testing.T) {
	tests := []struct {
		name   string
		cfg    client.Config
		server func(s script)
		run    func(ctx context.Context, c *client.Client) error
		stage  client.Stage
		cause  error
		accept wire.Accept
		msg    string
	}{
		{
			name:   "server refused",
			server: func(s script) { s.send(&wire.ServerGreeting{Modes: 0, Count: 2048}) },
			stage:  client.StageGreeting, cause: client.ErrServerRefused,
			msg: "greeting: server refused the connection (Modes 0)",
		},
		{
			name: "no common mode",
			server: func(s script) {
				s.send(&wire.ServerGreeting{Modes: wire.ModeAuthenticated | wire.ModeEncrypted, Count: 2048})
				var setup wire.SetUpResponse
				_ = setup.Decode(s.recv(wire.SetUpResponseSize))
				if setup.Mode != 0 {
					s.t.Errorf("Set-Up-Response mode = %v, want 0", setup.Mode)
				}
			},
			stage: client.StageGreeting, cause: client.ErrNoCommonMode,
			msg: "greeting: no common mode: server offers authenticated|encrypted, client accepts unauthenticated",
		},
		{
			name:   "count too large",
			server: func(s script) { s.send(&wire.ServerGreeting{Modes: 1, Count: 65536}) },
			stage:  client.StageGreeting, cause: client.ErrCountTooLarge,
		},
		{
			name: "count within a raised limit",
			cfg:  client.Config{MaxCount: 1 << 20},
			server: func(s script) {
				s.send(&wire.ServerGreeting{Modes: 1, Count: 65536})
				s.recv(wire.SetUpResponseSize)
				s.send(&wire.ServerStart{Accept: wire.AcceptFailure})
			},
			stage: client.StageSetup, cause: client.ErrRejected, accept: wire.AcceptFailure,
			msg: "setup: rejected by server: server-start accept=1 (failure)",
		},
		{
			name: "accept-session rejected",
			server: func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				s.send(&wire.AcceptSession{Accept: wire.AcceptTemporaryLimit})
			},
			run: func(ctx context.Context, c *client.Client) error {
				_, err := c.Request(ctx, client.Request{SenderPort: 1})
				return err
			},
			stage: client.StageRequest, cause: client.ErrRejected, accept: wire.AcceptTemporaryLimit,
			msg: "request: rejected by server: accept-session accept=5 (temporary resource limitation)",
		},
		{
			name: "reserved accept value",
			server: func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				s.send(&wire.AcceptSession{Accept: 6})
			},
			run: func(ctx context.Context, c *client.Client) error {
				_, err := c.Request(ctx, client.Request{SenderPort: 1})
				return err
			},
			stage: client.StageRequest, cause: client.ErrRejected, accept: 6,
			msg: "request: rejected by server: accept-session accept=6 (failure)",
		},
		{
			name: "alternate port refused",
			cfg:  client.Config{RejectAlternatePort: true},
			server: func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				s.send(&wire.AcceptSession{Port: 20001})
			},
			run: func(ctx context.Context, c *client.Client) error {
				_, err := c.Request(ctx, client.Request{SenderPort: 1})
				return err
			},
			stage: client.StageRequest, cause: client.ErrAlternatePort,
		},
		{
			name: "start-ack rejected",
			server: func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				s.send(&wire.AcceptSession{Port: 1})
				s.recv(wire.StartSessionsSize)
				s.send(&wire.StartAck{Accept: wire.AcceptInternalError})
			},
			run: func(ctx context.Context, c *client.Client) error {
				if _, err := c.Request(ctx, client.Request{SenderPort: 1}); err != nil {
					return err
				}
				return c.Start(ctx)
			},
			stage: client.StageStart, cause: client.ErrRejected, accept: wire.AcceptInternalError,
		},
		{
			name:   "connection closed during greeting",
			server: func(s script) { _ = s.conn.Close() },
			stage:  client.StageGreeting, cause: io.EOF,
		},
		{
			name: "connection closed before accept-session",
			server: func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				_ = s.conn.Close()
			},
			run: func(ctx context.Context, c *client.Client) error {
				_, err := c.Request(ctx, client.Request{SenderPort: 1})
				return err
			},
			stage: client.StageRequest, cause: io.EOF,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, sc := pipe(clientAddr, serverAddr)
			wait := serve(t, sc, tt.server)
			c, err := client.Handshake(t.Context(), cc, tt.cfg)
			if err == nil && tt.run != nil {
				err = tt.run(t.Context(), c)
			}
			var ce *client.Error
			if !errors.As(err, &ce) || ce.Stage != tt.stage || !errors.Is(err, tt.cause) || ce.Accept != tt.accept {
				t.Fatalf("err = %#v (%v), want stage %v cause %v accept %d", err, err, tt.stage, tt.cause, tt.accept)
			}
			if tt.msg != "" && err.Error() != tt.msg {
				t.Errorf("message %q, want %q", err.Error(), tt.msg)
			}
			// The connection is closed: the server end sees EOF.
			_ = sc.SetReadDeadline(time.Now().Add(time.Second))
			if _, rerr := sc.Read(make([]byte, 1)); !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrClosedPipe) {
				t.Errorf("server end read %v, want EOF", rerr)
			}
			wait()
		})
	}
}

func TestTimeouts(t *testing.T) {
	t.Run("greeting", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			cc, _ := pipe(clientAddr, serverAddr)
			start := time.Now()
			_, err := client.Handshake(t.Context(), cc, client.Config{})
			if !errors.Is(err, client.ErrTimeout) || stageOf(err) != client.StageGreeting {
				t.Fatalf("err = %v", err)
			}
			if d := time.Since(start); d != client.DefaultTimeout {
				t.Errorf("timed out after %v, want %v", d, client.DefaultTimeout)
			}
			if err.Error() != "control timeout in greeting (after 15s)" {
				t.Errorf("message %q", err.Error())
			}
		})
	})
	t.Run("start-ack", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			cc, sc := pipe(clientAddr, serverAddr)
			wait := serve(t, sc, func(s script) {
				s.handshake()
				s.recv(wire.RequestTWSessionSize)
				s.send(&wire.AcceptSession{Port: 1})
				s.recv(wire.StartSessionsSize)
				time.Sleep(11 * time.Second) // an ACX-like slow Start-Ack within the limit
				s.send(&wire.StartAck{})
			})
			c, err := client.Handshake(t.Context(), cc, client.Config{Timeout: 12 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Request(t.Context(), client.Request{SenderPort: 1}); err != nil {
				t.Fatal(err)
			}
			if err := c.Start(t.Context()); err != nil {
				t.Fatalf("slow Start-Ack: %v", err)
			}
			wait()
			_ = c.Close()
		})
	})
	t.Run("write", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			cc, sc := pipe(clientAddr, serverAddr)
			// The server sends the greeting but never reads.
			go func() { _, _ = sc.Write((&wire.ServerGreeting{Modes: 1, Count: 2048}).Append(nil)) }()
			_, err := client.Handshake(t.Context(), cc, client.Config{Timeout: time.Second})
			if !errors.Is(err, client.ErrTimeout) || stageOf(err) != client.StageSetup {
				t.Fatalf("err = %v", err)
			}
		})
	})
}

func TestContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cc, _ := pipe(clientAddr, serverAddr)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(time.Second, cancel)
		_, err := client.Handshake(ctx, cc, client.Config{})
		if !errors.Is(err, context.Canceled) || stageOf(err) != client.StageGreeting {
			t.Fatalf("err = %v", err)
		}
	})
}

// activeClient returns a client in ACTIVE state and the server end.
func activeClient(t *testing.T) (*client.Client, net.Conn) {
	t.Helper()
	cc, sc := pipe(clientAddr, serverAddr)
	wait := serve(t, sc, func(s script) {
		s.handshake()
		s.recv(wire.RequestTWSessionSize)
		s.send(&wire.AcceptSession{Port: 1})
		s.recv(wire.StartSessionsSize)
		s.send(&wire.StartAck{})
	})
	c, err := client.Handshake(t.Context(), cc, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	wait()
	return c, sc
}

func TestControlLost(t *testing.T) {
	t.Run("server closes", func(t *testing.T) {
		c, sc := activeClient(t)
		_ = sc.Close()
		<-c.Lost()
		if !errors.Is(c.Err(), client.ErrControlLost) || stageOf(c.Err()) != client.StageTest {
			t.Errorf("Err() = %v", c.Err())
		}
		if err := c.Stop(t.Context(), wire.AcceptOK); !errors.Is(err, client.ErrControlLost) {
			t.Errorf("Stop after loss = %v", err)
		}
		if c.State() != client.StateClosed {
			t.Errorf("state = %v", c.State())
		}
	})
	t.Run("server talks while active", func(t *testing.T) {
		c, sc := activeClient(t)
		go func() { _, _ = sc.Write([]byte{1}) }()
		<-c.Lost()
		if err := c.Err(); !errors.Is(err, client.ErrControlLost) || !strings.Contains(err.Error(), "unexpected message") {
			t.Errorf("Err() = %v", err)
		}
		_ = c.Close()
	})
	t.Run("close while active", func(t *testing.T) {
		c, _ := activeClient(t)
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func TestStateErrors(t *testing.T) {
	c, sc := activeClient(t)
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 1}); !errors.Is(err, client.ErrState) {
		t.Errorf("Request while active: %v", err)
	}
	if err := c.Start(t.Context()); !errors.Is(err, client.ErrState) {
		t.Errorf("Start while active: %v", err)
	}
	go func() { _, _ = io.ReadFull(sc, make([]byte, wire.StopSessionsSize)) }()
	if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(t.Context(), wire.AcceptOK); !errors.Is(err, client.ErrState) {
		t.Errorf("Stop while idle: %v", err)
	}
	if err := c.Start(t.Context()); !errors.Is(err, client.ErrState) {
		t.Errorf("Start without sessions: %v", err)
	}
	if c.Lost() == nil {
		t.Error("Lost() is nil after a run")
	}
	_ = c.Close()
}

func TestStageString(t *testing.T) {
	for s, want := range map[client.Stage]string{
		client.StageConnect: "connect", client.StageGreeting: "greeting", client.StageSetup: "setup",
		client.StageRequest: "request", client.StageStart: "start", client.StageTest: "test", client.StageStop: "stop",
		0: "Stage(0)", 99: "Stage(99)",
	} {
		if s.String() != want {
			t.Errorf("Stage(%d).String() = %q, want %q", s, s.String(), want)
		}
	}
}

// TestAcceptSessionPortZero checks that an Accept-Session with Accept 0 but
// no port is a request failure (xreview m2).
func TestAcceptSessionPortZero(t *testing.T) {
	cc, sc := pipe(clientAddr, serverAddr)
	wait := serve(t, sc, func(s script) {
		s.handshake()
		s.recv(wire.RequestTWSessionSize)
		s.send(&wire.AcceptSession{Port: 0, SID: wire.SID{1}})
	})
	c, err := client.Handshake(t.Context(), cc, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 1}); !errors.Is(err, client.ErrInvalidPort) || stageOf(err) != client.StageRequest {
		t.Errorf("err = %v, want ErrInvalidPort at request", err)
	}
	wait()
}

// secureScript plays a server in a protected mode, built from the wire
// primitives alone, for the client's side of the authenticated modes.
type secureScript struct {
	script
	g     wire.ServerGreeting
	setup wire.SetUpResponse
	sec   *wire.ControlCrypto
}

// accept reads Set-Up-Response, checks the Token under passphrase and
// answers Server-Start with a protected Start-Time block.
func (s *secureScript) accept(passphrase string, accept wire.Accept) {
	s.t.Helper()
	_ = s.setup.Decode(s.recv(wire.SetUpResponseSize))
	key, _ := wire.DeriveKey([]byte(passphrase), s.g.Salt, s.g.Count)
	challenge, keys := wire.DecryptToken(key, s.setup.Token)
	if challenge != s.g.Challenge {
		accept = wire.AcceptFailure
	}
	ss := wire.ServerStart{Accept: accept, ServerIV: [16]byte{9, 9, 9}, StartTime: ntptime.New(100, 0)}
	out := ss.Append(nil)
	if accept == wire.AcceptOK {
		s.sec = wire.NewControlCrypto(keys, ss.ServerIV, s.setup.ClientIV)
		_ = s.sec.Seal(out[32:48], false)
	} else {
		clear(out[32:40])
	}
	if _, err := s.conn.Write(out); err != nil {
		s.t.Errorf("server write: %v", err)
	}
}

// read reads and checks one protected client message of n octets.
func (s *secureScript) read(n int) []byte {
	s.t.Helper()
	b := s.recv(n)
	if err := s.sec.Open(b); err != nil {
		s.t.Error(err)
	}
	if err := s.sec.Verify(b); err != nil {
		s.t.Errorf("client message: %v", err)
	}
	return b
}

// write seals and sends m, with its last octet flipped when corrupt.
func (s *secureScript) write(m interface{ Append([]byte) []byte }, corrupt bool) {
	b := m.Append(nil)
	_ = s.sec.Seal(b, true)
	if corrupt {
		b[len(b)-1] ^= 1
	}
	if _, err := s.conn.Write(b); err != nil {
		s.t.Errorf("server write: %v", err)
	}
}

func newSecureScript(t *testing.T, conn net.Conn, modes wire.Modes, count uint32) *secureScript {
	return &secureScript{
		script: script{t, conn},
		g:      wire.ServerGreeting{Modes: modes, Count: count, Challenge: [16]byte{1, 2, 3}, Salt: [16]byte{4, 5, 6}},
	}
}

var encrypted = client.Config{Modes: wire.ModeEncrypted, KeyID: "alice", Passphrase: []byte("secretpass")}

// TestEncryptedExchange runs a whole exchange against an independent
// encrypting server and checks the test keys the client derives.
func TestEncryptedExchange(t *testing.T) {
	cc, sc := pipe(clientAddr, serverAddr)
	sid := wire.SID{7, 7, 7}
	var keysSeen wire.TestKeys
	wait := serve(t, sc, func(sp script) {
		s := newSecureScript(t, sp.conn, wire.ModeEncrypted|wire.ModeUnauthenticated, 2048)
		s.send(&s.g)
		s.accept("secretpass", wire.AcceptOK)
		if wire.KeyIDString(s.setup.KeyID) != "alice" || s.setup.Mode != wire.ModeEncrypted {
			t.Errorf("set-up-response %+v", s.setup)
		}
		var req wire.RequestTWSession
		_ = req.Decode(s.read(wire.RequestTWSessionSize))
		if req.SenderPort != 50000 {
			t.Errorf("request %+v", req)
		}
		s.write(&wire.AcceptSession{Port: 50000, SID: sid}, false)
		s.read(wire.StartSessionsSize)
		s.write(&wire.StartAck{}, false)
		s.read(wire.StopSessionsSize)
		key, _ := wire.DeriveKey([]byte("secretpass"), s.g.Salt, s.g.Count)
		_, keys := wire.DecryptToken(key, s.setup.Token)
		keysSeen = wire.DeriveTestKeys(keys, sid)
	})
	c, err := client.Handshake(t.Context(), cc, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if c.Security() != wire.SecurityEncrypted || c.ServerStartTime() != ntptime.New(100, 0) {
		t.Errorf("security %v start %v", c.Security(), c.ServerStartTime())
	}
	sess, err := c.Request(t.Context(), client.Request{SenderPort: 50000})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	wait()
	if sess.Security != wire.SecurityEncrypted || sess.TestKeys != keysSeen || keysSeen == (wire.TestKeys{}) {
		t.Errorf("session %v keys %x, server derived %x", sess.Security, sess.TestKeys, keysSeen)
	}
	_ = c.Close()
}

func TestAuthFailures(t *testing.T) {
	t.Run("server refuses the token", func(t *testing.T) {
		cc, sc := pipe(clientAddr, serverAddr)
		wait := serve(t, sc, func(sp script) {
			s := newSecureScript(t, sp.conn, wire.ModeEncrypted, 2048)
			s.send(&s.g)
			s.accept("another secret", wire.AcceptOK)
		})
		_, err := client.Handshake(t.Context(), cc, encrypted)
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Stage != client.StageSetup || ce.Accept != wire.AcceptFailure {
			t.Errorf("err = %v, want Accept 1 at setup", err)
		}
		wait()
	})
	t.Run("corrupted accept-session", func(t *testing.T) {
		cc, sc := pipe(clientAddr, serverAddr)
		wait := serve(t, sc, func(sp script) {
			s := newSecureScript(t, sp.conn, wire.ModeEncrypted, 2048)
			s.send(&s.g)
			s.accept("secretpass", wire.AcceptOK)
			s.read(wire.RequestTWSessionSize)
			s.write(&wire.AcceptSession{Port: 50000}, true)
		})
		c, err := client.Handshake(t.Context(), cc, encrypted)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); !errors.Is(err, wire.ErrIntegrity) || stageOf(err) != client.StageRequest {
			t.Errorf("err = %v, want an integrity failure at request", err)
		}
		wait()
	})
	t.Run("count below 1024", func(t *testing.T) {
		cc, sc := pipe(clientAddr, serverAddr)
		wait := serve(t, sc, func(sp script) {
			s := newSecureScript(t, sp.conn, wire.ModeEncrypted, 512)
			s.send(&s.g)
			var setup wire.SetUpResponse
			_ = setup.Decode(s.recv(wire.SetUpResponseSize))
			if setup.Mode != 0 {
				t.Errorf("client chose %v, want 0", setup.Mode)
			}
		})
		if _, err := client.Handshake(t.Context(), cc, encrypted); !errors.Is(err, client.ErrCountTooSmall) {
			t.Errorf("err = %v, want ErrCountTooSmall", err)
		}
		wait()
	})
	for _, tt := range []struct {
		name string
		cfg  client.Config
		want string
	}{
		{"no key id", client.Config{Modes: wire.ModeAuthenticated, Passphrase: []byte("x")}, "need a key id"},
		{"no passphrase", client.Config{Modes: wire.ModeMixed, KeyID: "alice"}, "need a passphrase"},
		{"feature bit", client.Config{Modes: wire.ModeUnauthenticated | wire.ModeReflectOctets}, "only the security modes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cc, sc := pipe(clientAddr, serverAddr)
			defer sc.Close()
			_, err := client.Handshake(t.Context(), cc, tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) || stageOf(err) != client.StageSetup {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestReflectedOctetsMismatch checks that a server not returning the Octets
// to be reflected (RFC 6038 §4.3) fails the request.
func TestReflectedOctetsMismatch(t *testing.T) {
	cc, sc := pipe(clientAddr, serverAddr)
	wait := serve(t, sc, func(s script) {
		s.send(&wire.ServerGreeting{Modes: wire.ModeUnauthenticated | wire.ModeReflectOctets, Count: 2048})
		var setup wire.SetUpResponse
		_ = setup.Decode(s.recv(wire.SetUpResponseSize))
		if setup.Mode != wire.ModeUnauthenticated|wire.ModeReflectOctets {
			t.Errorf("mode %v", setup.Mode)
		}
		s.send(&wire.ServerStart{StartTime: ntptime.New(100, 0)})
		var req wire.RequestTWSession
		_ = req.Decode(s.recv(wire.RequestTWSessionSize))
		if req.OctetsToReflect != 0xbeef || req.PaddingToReflect != 4 {
			t.Errorf("request %+v", req)
		}
		s.send(&wire.AcceptSession{Port: 50000, ReflectedOctets: 0xdead})
	})
	c, err := client.Handshake(t.Context(), cc, client.Config{Features: client.FeatureModes})
	if err != nil {
		t.Fatal(err)
	}
	if c.Features() != wire.ModeReflectOctets {
		t.Errorf("features %v", c.Features())
	}
	if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000, PaddingLength: 40, OctetsToReflect: 0xbeef, PaddingToReflect: 4}); !errors.Is(err, client.ErrReflectedOctets) {
		t.Errorf("err = %v", err)
	}
	wait()
	if _, err := client.Handshake(t.Context(), cc, client.Config{Features: wire.ModeDSCPECNMonitoring}); err == nil || !strings.Contains(err.Error(), "only individual session control") {
		t.Errorf("unsupported feature: %v", err)
	}
}

// iscScript plays a server under Individual Session Control that accepts
// one session per request, numbered from 1.
func iscScript(s script, then func(s script)) {
	s.send(&wire.ServerGreeting{Modes: wire.ModeUnauthenticated | wire.ModeIndividualSessionControl, Count: 2048})
	s.recv(wire.SetUpResponseSize)
	s.send(&wire.ServerStart{StartTime: ntptime.New(100, 0)})
	for i := byte(1); i <= 2; i++ {
		s.recv(wire.RequestTWSessionSize)
		s.send(&wire.AcceptSession{Port: 50000, SID: wire.SID{i}})
	}
	then(s)
}

// recvN reads a Start-N-Sessions or Stop-N-Sessions of n sessions.
func recvN(s script, n int) wire.SessionControl {
	var m wire.SessionControl
	_ = m.Decode(s.recv(wire.SessionControlSize(n)))
	return m
}

func TestStartNStopN(t *testing.T) {
	for _, tt := range []struct {
		name    string
		server  func(s script)
		started int
		errIs   error
	}{
		{"split acks", func(s script) {
			if m := recvN(s, 2); m.Command != wire.CommandStartNSessions || len(m.SIDs) != 2 {
				t.Errorf("start-n %+v", m)
			}
			s.send(&wire.SessionControl{Command: wire.CommandStartNAck, SIDs: []wire.SID{{2}}})
			s.send(&wire.SessionControl{Command: wire.CommandStartNAck, Accept: wire.AcceptTemporaryLimit, SIDs: []wire.SID{{1}}})
			if m := recvN(s, 1); m.Command != wire.CommandStopNSessions || m.SIDs[0] != (wire.SID{2}) {
				t.Errorf("stop-n %+v", m)
			}
			s.send(&wire.SessionControl{Command: wire.CommandStopNAck, SIDs: []wire.SID{{2}}})
		}, 1, client.ErrRejected},
		{"ack for another session", func(s script) {
			recvN(s, 2)
			s.send(&wire.SessionControl{Command: wire.CommandStartNAck, SIDs: []wire.SID{{9}}})
		}, 0, client.ErrUnexpectedMessage},
		{"wrong acknowledgement", func(s script) {
			recvN(s, 2)
			s.send(&wire.SessionControl{Command: wire.CommandStopNAck, SIDs: []wire.SID{{1}}})
		}, 0, client.ErrUnexpectedMessage},
		{"more sessions than asked", func(s script) {
			recvN(s, 2)
			// The client gives up after the header.
			_, _ = s.conn.Write((&wire.SessionControl{Command: wire.CommandStartNAck, SIDs: []wire.SID{{1}, {2}, {3}}}).Append(nil))
		}, 0, client.ErrUnexpectedMessage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cc, sc := pipe(clientAddr, serverAddr)
			wait := serve(t, sc, func(s script) { iscScript(s, tt.server) })
			c, err := client.Handshake(t.Context(), cc, client.Config{Features: wire.ModeIndividualSessionControl})
			if err != nil {
				t.Fatal(err)
			}
			s1, _ := c.Request(t.Context(), client.Request{SenderPort: 1})
			s2, _ := c.Request(t.Context(), client.Request{SenderPort: 2})
			started, err := c.StartN(t.Context(), []*client.Session{s1, s2})
			if len(started) != tt.started || !errors.Is(err, tt.errIs) {
				t.Errorf("StartN = %d sessions, %v", len(started), err)
			}
			if tt.started == 1 {
				if c.State() != client.StateActive || len(c.Started()) != 1 {
					t.Errorf("state %v, started %d", c.State(), len(c.Started()))
				}
				if _, err := c.StopN(t.Context(), started); err != nil || c.State() != client.StateIdle {
					t.Errorf("StopN: %v, state %v", err, c.State())
				}
			}
			_ = c.Close()
			wait()
		})
	}
	// Without the mode negotiated, the RFC 5938 calls are refused.
	cc, sc := pipe(clientAddr, serverAddr)
	wait := serve(t, sc, func(s script) { s.handshake() })
	c, err := client.Handshake(t.Context(), cc, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartN(t.Context(), []*client.Session{{}}); !errors.Is(err, client.ErrState) {
		t.Errorf("StartN without the mode: %v", err)
	}
	if _, err := c.StopN(t.Context(), []*client.Session{{}}); !errors.Is(err, client.ErrState) {
		t.Errorf("StopN without the mode: %v", err)
	}
	_ = c.Close()
	wait()
}
