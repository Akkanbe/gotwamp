//go:build linux

package sender_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// fullServer runs a control server with the Full reflector on loopback and
// returns its address.
func fullServer(t *testing.T, cfg server.Config) (netip.AddrPort, *server.Server) {
	t.Helper()
	addr, srv, _ := fullServerReflector(t, cfg)
	return addr, srv
}

// fullServerReflector is fullServer that also returns the Full reflector.
func fullServerReflector(t *testing.T, cfg server.Config) (netip.AddrPort, *server.Server, *reflector.Full) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(cfg, full, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return ln.Addr().(*net.TCPAddr).AddrPort(), srv, full
}

func fullConfig(count uint32) sender.Config {
	cfg := sender.DefaultConfig(netip.AddrPort{})
	cfg.Count = count
	cfg.Interval = 5 * time.Millisecond
	cfg.Timeout = 500 * time.Millisecond
	return cfg
}

func TestRunFull(t *testing.T) {
	addr, srv := fullServer(t, server.Config{TestPorts: server.PortRange{Low: 42000, High: 42999}})
	var packets []sender.PacketResult
	hooks := sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) { packets = append(packets, r) }})
	sum, err := sender.RunFull(t.Context(), fullConfig(20), sender.FullConfig{Server: addr, EndDelay: 10 * time.Millisecond}, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Sent != 20 || sum.Received != 20 || sum.Lost != 0 || sum.Aborted {
		t.Errorf("summary = %+v", sum.Summary)
	}
	if sum.Remote.Addr() != addr.Addr() || sum.Local.Addr() != addr.Addr() || sum.Local.Port() < 49152 || sum.Local.Port() > 57343 {
		t.Errorf("local %v remote %v", sum.Local, sum.Remote)
	}
	// The requested Receiver Port equals the Sender Port, which the sender
	// holds itself on this host: the server must offer an alternate port.
	if sum.Remote.Port() < 42000 || sum.Remote.Port() > 42999 {
		t.Errorf("reflector port %d is not an alternate one", sum.Remote.Port())
	}
	for i, p := range packets {
		if p.Record.ReflectorSeq != uint32(i) || p.Record.SenderSeq != uint32(i) {
			t.Errorf("packet %d: reflector seq %d sender seq %d", i, p.Record.ReflectorSeq, p.Record.SenderSeq)
		}
	}
	// Stop-Sessions was sent: the session drains for its Timeout and goes.
	waitFor(t, func() bool { return len(srv.Sessions()) == 0 })
}

func TestRunFullFixedPorts(t *testing.T) {
	addr, _ := fullServer(t, server.Config{})
	cfg := fullConfig(5)
	cfg.DSCP = 46
	cfg.Padding = 100
	fc := sender.FullConfig{Server: addr, SenderPort: freePort(t), ReceiverPort: freePort(t), StartTimeNow: true, ZeroAddresses: true, ControlDSCP: 34}
	sum, err := sender.RunFull(t.Context(), cfg, fc)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Received != 5 || sum.Local.Port() != fc.SenderPort || sum.Remote.Port() != fc.ReceiverPort {
		t.Errorf("summary %+v local %v remote %v", sum.Summary, sum.Local, sum.Remote)
	}
	// The fixed Sender Port is now free again; holding it makes a run fail.
	hold, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(addr.Addr(), fc.SenderPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if _, err := sender.RunFull(t.Context(), cfg, fc); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("run with a taken Sender Port: %v", err)
	}
}

func TestRunFullCanceled(t *testing.T) {
	addr, srv := fullServer(t, server.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	cfg := fullConfig(0) // continuous until canceled
	cfg.Window = time.Second
	hooks := sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) {
		if r.Record.SenderSeq == 10 {
			cancel()
		}
	}})
	sum, err := sender.RunFull(ctx, cfg, sender.FullConfig{Server: addr}, hooks)
	if !errors.Is(err, context.Canceled) || !sum.Aborted {
		t.Fatalf("err %v, aborted %v", err, sum.Aborted)
	}
	waitFor(t, func() bool { return len(srv.Sessions()) == 0 })
}

// TestRunFullCanceledSendsStop checks on the wire that a canceled run still
// sends Stop-Sessions, with Accept 0 (a normal, if premature, end).
func TestRunFullCanceledSendsStop(t *testing.T) {
	stop := make(chan wire.StopSessions, 1)
	fc := scripted(func(c net.Conn) {
		handshake(c)
		recv(c, wire.RequestTWSessionSize)
		send(c, &wire.AcceptSession{Port: 9})
		recv(c, wire.StartSessionsSize)
		send(c, &wire.StartAck{})
		var s wire.StopSessions
		_ = s.Decode(recv(c, wire.StopSessionsSize))
		stop <- s
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := sender.RunFull(ctx, fullConfig(0), fc); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	select {
	case s := <-stop:
		if s.Accept != wire.AcceptOK || s.Sessions != 1 {
			t.Errorf("Stop-Sessions = %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Stop-Sessions after cancel")
	}
}

func TestRunFullRejected(t *testing.T) {
	addr, _ := fullServer(t, server.Config{MaxPadding: 10})
	cfg := fullConfig(1)
	cfg.Padding = 27
	_, err := sender.RunFull(t.Context(), cfg, sender.FullConfig{Server: addr})
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Stage != client.StageRequest || ce.Accept != wire.AcceptNotSupported {
		t.Fatalf("err = %v", err)
	}
}

// scripted runs a TWAMP-Control server script on the far end of a pipe that
// RunFull dials, with the test socket on loopback.
func scripted(script func(conn net.Conn)) sender.FullConfig {
	return sender.WithFullDial(sender.FullConfig{
		Server: netip.MustParseAddrPort("127.0.0.1:862"),

		ControlTimeout: time.Second,
	}, func(_ context.Context, _ netip.Addr, server netip.AddrPort) (net.Conn, error) {
		c, s := net.Pipe()
		go func() {
			defer s.Close()
			script(s)
		}()
		local := net.TCPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:40000"))
		return tcpAddrs{c, local, net.TCPAddrFromAddrPort(server)}, nil
	})
}

type tcpAddrs struct {
	net.Conn
	local, remote net.Addr
}

func (c tcpAddrs) LocalAddr() net.Addr  { return c.local }
func (c tcpAddrs) RemoteAddr() net.Addr { return c.remote }

func send(conn net.Conn, m interface{ Append([]byte) []byte }) { _, _ = conn.Write(m.Append(nil)) }

func recv(conn net.Conn, n int) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(conn, b)
	return b
}

func handshake(conn net.Conn) {
	send(conn, &wire.ServerGreeting{Modes: wire.ModeUnauthenticated, Count: 2048})
	recv(conn, wire.SetUpResponseSize)
	send(conn, &wire.ServerStart{})
}

func TestRunFullControlErrors(t *testing.T) {
	tests := []struct {
		name   string
		fc     sender.FullConfig
		stage  client.Stage
		cause  error
		accept wire.Accept
	}{
		{
			name: "connect failure",
			fc: sender.WithFullDial(sender.FullConfig{Server: netip.MustParseAddrPort("127.0.0.1:862")}, func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
				return nil, syscall.ECONNREFUSED
			}),
			stage: client.StageConnect, cause: syscall.ECONNREFUSED,
		},
		{
			name:  "server refused",
			fc:    scripted(func(c net.Conn) { send(c, &wire.ServerGreeting{Count: 2048}) }),
			stage: client.StageGreeting, cause: client.ErrServerRefused,
		},
		{
			name: "no common mode",
			fc: scripted(func(c net.Conn) {
				send(c, &wire.ServerGreeting{Modes: wire.ModeEncrypted, Count: 2048})
				recv(c, wire.SetUpResponseSize)
			}),
			stage: client.StageGreeting, cause: client.ErrNoCommonMode,
		},
		{
			name: "control timeout",
			fc: scripted(func(c net.Conn) {
				handshake(c)
				recv(c, wire.RequestTWSessionSize)
				recv(c, 1) // never answers
			}),
			stage: client.StageRequest, cause: client.ErrTimeout,
		},
		{
			name: "start-ack rejected",
			fc: scripted(func(c net.Conn) {
				handshake(c)
				recv(c, wire.RequestTWSessionSize)
				send(c, &wire.AcceptSession{Port: 9})
				recv(c, wire.StartSessionsSize)
				send(c, &wire.StartAck{Accept: wire.AcceptPermanentLimit})
			}),
			stage: client.StageStart, cause: client.ErrRejected, accept: wire.AcceptPermanentLimit,
		},
		{
			name: "control lost during the run",
			fc: scripted(func(c net.Conn) {
				handshake(c)
				recv(c, wire.RequestTWSessionSize)
				send(c, &wire.AcceptSession{Port: 9})
				recv(c, wire.StartSessionsSize)
				send(c, &wire.StartAck{})
				time.Sleep(50 * time.Millisecond)
			}),
			stage: client.StageTest, cause: client.ErrControlLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fullConfig(1000)
			_, err := sender.RunFull(t.Context(), cfg, tt.fc)
			var ce *client.Error
			if !errors.As(err, &ce) || ce.Stage != tt.stage || !errors.Is(err, tt.cause) || ce.Accept != tt.accept {
				t.Fatalf("err = %v, want stage %v cause %v accept %d", err, tt.stage, tt.cause, tt.accept)
			}
		})
	}
}

func TestRunFullStopAfterSocketError(t *testing.T) {
	stop := make(chan wire.StopSessions, 1)
	fc := scripted(func(c net.Conn) {
		handshake(c)
		recv(c, wire.RequestTWSessionSize)
		send(c, &wire.AcceptSession{Port: 9})
		recv(c, wire.StartSessionsSize)
		send(c, &wire.StartAck{})
		var s wire.StopSessions
		_ = s.Decode(recv(c, wire.StopSessionsSize))
		stop <- s
	})
	fc = sender.WithFullListen(fc, func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
		c, err := tstamp.Listen(network, local, opts)
		if err != nil {
			return nil, err
		}
		return failingRead{c}, nil
	})
	_, err := sender.RunFull(t.Context(), fullConfig(1000), fc)
	if err == nil || errors.As(err, new(*client.Error)) {
		t.Fatalf("err = %v, want the socket error", err)
	}
	select {
	case s := <-stop:
		if s.Accept != wire.AcceptFailure || s.Sessions != 1 {
			t.Errorf("Stop-Sessions = %+v, want accept 1 for 1 session", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Stop-Sessions")
	}
}

// failingRead is a socket whose reads fail.
type failingRead struct{ tstamp.Conn }

func (failingRead) ReadFrom([]byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	return 0, netip.AddrPort{}, tstamp.RxMeta{}, errors.New("socket broken")
}

func TestRunFullSenderPortScan(t *testing.T) {
	addr, _ := fullServer(t, server.Config{})
	var tried []uint16
	fc := sender.FullConfig{Server: addr, SenderPorts: sender.PortRange{Low: 49152, High: 65535}}
	fc = sender.WithFullListen(fc, func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
		tried = append(tried, local.Port())
		if len(tried) < 3 {
			return nil, &net.OpError{Op: "listen", Err: syscall.EADDRINUSE}
		}
		return tstamp.Listen(network, local, opts)
	})
	sum, err := sender.RunFull(t.Context(), fullConfig(3), fc)
	if err != nil {
		t.Fatal(err)
	}
	if len(tried) != 3 || sum.Local.Port() != tried[2] {
		t.Errorf("tried %v, local %v", tried, sum.Local)
	}
	fc.SenderPorts = sender.PortRange{Low: 50000, High: 50000}
	tried = nil
	fc = sender.WithFullListen(fc, func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) {
		return nil, syscall.EADDRINUSE
	})
	if _, err := sender.RunFull(t.Context(), fullConfig(3), fc); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("exhausted range: %v", err)
	}
}

func TestRunFullInvalid(t *testing.T) {
	ok := netip.MustParseAddrPort("127.0.0.1:862")
	for name, tc := range map[string]struct {
		cfg sender.Config
		fc  sender.FullConfig
	}{
		"no server":       {fullConfig(1), sender.FullConfig{}},
		"control dscp":    {fullConfig(1), sender.FullConfig{Server: ok, ControlDSCP: 64}},
		"port range":      {fullConfig(1), sender.FullConfig{Server: ok, SenderPorts: sender.PortRange{Low: 2, High: 1}}},
		"end delay":       {fullConfig(1), sender.FullConfig{Server: ok, EndDelay: -1}},
		"test dscp":       {func() sender.Config { c := fullConfig(1); c.DSCP = 64; return c }(), sender.FullConfig{Server: ok}},
		"mismatched host": {func() sender.Config { c := fullConfig(1); c.Source = netip.MustParseAddrPort("[::1]:0"); return c }(), sender.FullConfig{Server: ok}},
	} {
		dialed := false
		tc.fc = sender.WithFullDial(tc.fc, func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
			dialed = true
			return nil, errors.New("dialed")
		})
		if _, err := sender.RunFull(t.Context(), tc.cfg, tc.fc); err == nil || dialed {
			t.Errorf("%s: err %v, dialed %v", name, err, dialed)
		}
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).AddrPort().Port()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunFullControlLostIsInvalid: results collected when the control
// connection was lost are marked invalid, in the returned summary and in the
// one the hook gets (SPEC §5.6).
func TestRunFullControlLostIsInvalid(t *testing.T) {
	fc := scripted(func(c net.Conn) {
		handshake(c)
		recv(c, wire.RequestTWSessionSize)
		send(c, &wire.AcceptSession{Port: 9})
		recv(c, wire.StartSessionsSize)
		send(c, &wire.StartAck{})
		time.Sleep(50 * time.Millisecond)
	})
	var hooked []sender.Summary
	hooks := sender.WithHooks(sender.Hooks{Summary: func(s sender.Summary) { hooked = append(hooked, s) }})
	sum, err := sender.RunFull(t.Context(), fullConfig(1000), fc, hooks)
	if !errors.Is(err, client.ErrControlLost) {
		t.Fatalf("err = %v", err)
	}
	if !sum.Invalid || !sum.Aborted {
		t.Errorf("summary invalid %v, aborted %v", sum.Invalid, sum.Aborted)
	}
	if len(hooked) != 1 || !hooked[0].Invalid || hooked[0].SID != sum.SID {
		t.Errorf("hooked summaries %+v", hooked)
	}
}

// TestRunFullSecurity runs a Full session in each protected mode against the
// control server and the Full reflector: test packets are authenticated or
// encrypted end to end, and unauthenticated in mixed mode (RFC 5618).
func TestRunFullSecurity(t *testing.T) {
	keys := keystore.Map{"alice": []byte("secretpass")}
	all := wire.SecurityModes
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted, wire.SecurityMixed} {
		for _, padding := range []int{0, 27, 64, 1000} {
			t.Run(fmt.Sprintf("%v/padding %d", mode, padding), func(t *testing.T) {
				addr, srv, full := fullServerReflector(t, server.Config{Modes: all, Keys: keys})
				cfg := fullConfig(20)
				cfg.Padding = padding
				var replies []sender.PacketResult
				hooks := sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) { replies = append(replies, r) }})
				fc := sender.FullConfig{Server: addr, Modes: mode.Mode(), KeyID: "alice", Passphrase: []byte("secretpass")}
				sum, err := sender.RunFull(t.Context(), cfg, fc, hooks)
				if err != nil {
					t.Fatal(err)
				}
				if sum.Sent != 20 || sum.Received != 20 || sum.Lost != 0 || sum.RTT.Min <= 0 {
					t.Errorf("summary = %+v", sum.Summary)
				}
				for i, r := range replies {
					if r.Record.SenderSeq != uint32(i) || r.Record.ReflectorSeq != uint32(i) || r.Record.T2.Before(r.Record.T1) || r.Record.T3.Before(r.Record.T2) {
						t.Errorf("reply %d: %+v", i, r.Record)
						break
					}
				}
				// The reflector counts a reply after sending it: the sender may
				// have it first.
				waitFor(t, func() bool { return full.Counters().Reflected == 20 })
				if c := full.Counters(); c.Dropped[reflector.DropMalformed] != 0 {
					t.Errorf("reflector counters %+v", c)
				}
				if s := srv.Sessions(); len(s) != 1 || s[0].Security != mode {
					t.Errorf("server sessions %+v", s)
				}
			})
		}
	}
	t.Run("wrong passphrase", func(t *testing.T) {
		addr, _, _ := fullServerReflector(t, server.Config{Modes: all, Keys: keys})
		fc := sender.FullConfig{Server: addr, Modes: wire.ModeEncrypted, KeyID: "alice", Passphrase: []byte("guess")}
		_, err := sender.RunFull(t.Context(), fullConfig(1), fc)
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Stage != client.StageSetup || ce.Accept != wire.AcceptFailure {
			t.Errorf("err = %v, want Accept 1 at setup", err)
		}
	})
}

// sizeConn records the lengths of the packets a socket sends and receives.
type sizeConn struct {
	tstamp.Conn
	mu       sync.Mutex
	sent     []int
	received []int
}

func (c *sizeConn) WriteTo(b []byte, to netip.AddrPort, tos int) (time.Time, error) {
	c.mu.Lock()
	c.sent = append(c.sent, len(b))
	c.mu.Unlock()
	return c.Conn.WriteTo(b, to, tos)
}

func (c *sizeConn) ReadFrom(b []byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	n, from, meta, err := c.Conn.ReadFrom(b)
	if err == nil {
		c.mu.Lock()
		c.received = append(c.received, n)
		c.mu.Unlock()
	}
	return n, from, meta, err
}

// TestRunFullExtensions runs Full sessions with the RFC 6038 and RFC 5938
// extensions against the control server and the Full reflector.
func TestRunFullExtensions(t *testing.T) {
	keys := keystore.Map{"alice": []byte("secretpass")}
	modes := wire.SecurityModes | server.FeatureModes
	for _, tt := range []struct {
		name              string
		features          wire.Modes
		security          wire.Modes
		padding           int
		octets, toReflect uint16
		sent, reply       int
	}{
		{"individual session control", wire.ModeIndividualSessionControl, 0, 27, 0, 0, 41, 41},
		{"symmetrical size", wire.ModeSymmetricalSize, 0, 8, 0, 0, 49, 49},
		{"reflect octets", wire.ModeReflectOctets, 0, 64, 0xbeef, 8, 78, 78},
		{"all", client.FeatureModes, 0, 16, 0x1234, 8, 57, 57},
		{"all, encrypted", client.FeatureModes, wire.ModeEncrypted, 16, 0x1234, 8, 128, 128},
		{"reflect octets, authenticated", wire.ModeReflectOctets, wire.ModeAuthenticated, 72, 0, 8, 120, 120},
	} {
		t.Run(tt.name, func(t *testing.T) {
			addr, srv, full := fullServerReflector(t, server.Config{Modes: modes, Keys: keys})
			cfg := fullConfig(10)
			cfg.Padding = tt.padding
			var sc *sizeConn
			fc := sender.WithFullListen(sender.FullConfig{
				Server: addr, Features: tt.features, OctetsToReflect: tt.octets, PaddingToReflect: tt.toReflect,
				Modes: tt.security, KeyID: "alice", Passphrase: []byte("secretpass"),
			}, func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
				c, err := tstamp.Listen(network, local, opts)
				if err != nil {
					return nil, err
				}
				sc = &sizeConn{Conn: c}
				return sc, nil
			})
			sum, err := sender.RunFull(t.Context(), cfg, fc)
			if err != nil {
				t.Fatal(err)
			}
			if sum.Sent != 10 || sum.Received != 10 || sum.Lost != 0 || sum.Malformed != 0 || sum.ReflectedMismatch != 0 {
				t.Errorf("summary %+v", sum.Summary)
			}
			sc.mu.Lock()
			if sc.sent[0] != tt.sent || sc.received[0] != tt.reply {
				t.Errorf("packets of %d octets, replies of %d; want %d and %d", sc.sent[0], sc.received[0], tt.sent, tt.reply)
			}
			sc.mu.Unlock()
			waitFor(t, func() bool { return full.Counters().Reflected == 10 })
			// Stop-Sessions has no acknowledgement: the server may take a moment.
			waitFor(t, func() bool {
				s := srv.Sessions()
				return len(s) == 1 && s[0].State == server.SessionDraining
			})
		})
	}
}

// TestSTAMPLoopback runs a STAMP session against the Light reflector in
// STAMP mode over loopback: the SSID and the Extra Padding TLV come back.
func TestSTAMPLoopback(t *testing.T) {
	light, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}, STAMP: true}, reflector.Deps{})
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error)
	go func() { served <- light.Serve(ctx) }()
	defer func() { cancel(); <-served }()

	cfg := fullConfig(10)
	cfg.Target = light.Addrs()[0]
	cfg.STAMP, cfg.SSID, cfg.Padding = true, 0x5a5a, 20
	var replies []sender.PacketResult
	s, err := sender.New(cfg, sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) { replies = append(replies, r) }}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum, err := s.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Received != 10 || sum.Malformed != 0 || sum.ForeignSource != 0 || sum.Lost != 0 {
		t.Errorf("summary %+v", sum.Summary)
	}
	if len(replies) != 10 {
		t.Errorf("%d replies", len(replies))
	}
	if c := light.Status().Counters; c.Reflected != 10 {
		t.Errorf("reflector counters %+v", c)
	}
}
