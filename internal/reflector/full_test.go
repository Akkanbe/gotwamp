package reflector_test

import (
	"bytes"
	"errors"
	"net/netip"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

var (
	fullLocal  = netip.MustParseAddrPort("198.51.100.2:20000")
	fullSender = netip.MustParseAddrPort("192.0.2.1:50000")
)

// fullHarness opens Full sessions over fake sockets inside a synctest bubble.
type fullHarness struct {
	t     *testing.T
	full  *reflector.Full
	conns []*fakeConn
	nets  []string
}

func newFull(t *testing.T, cfg reflector.FullConfig) *fullHarness {
	t.Helper()
	h := &fullHarness{t: t}
	listen := func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
		c := &fakeConn{local: local, opts: opts, mode: tstamp.ModeUserspace, in: make(chan rxPacket, 64), closed: make(chan struct{})}
		h.conns = append(h.conns, c)
		h.nets = append(h.nets, network)
		return c, nil
	}
	f, err := reflector.NewFull(cfg, reflector.Deps{Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	h.full = f
	return h
}

func (h *fullHarness) open(spec server.SessionSpec) (server.Session, *fakeConn) {
	h.t.Helper()
	if spec.Local == (netip.AddrPort{}) {
		spec.Local = fullLocal
	}
	if spec.Sender == (netip.AddrPort{}) {
		spec.Sender = fullSender
	}
	s, err := h.full.Open(spec)
	if err != nil {
		h.t.Fatal(err)
	}
	c := h.conns[len(h.conns)-1]
	h.t.Cleanup(func() { _ = s.Close() })
	return s, c
}

// send delivers a datagram arriving now and returns the replies.
func send(c *fakeConn, b []byte, from netip.AddrPort) []txPacket {
	c.in <- rxPacket{b: b, from: from, meta: tstamp.RxMeta{At: time.Now(), TTL: 64, TOS: -1}}
	synctest.Wait()
	return c.sent()
}

func TestFullLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{})
		s, c := h.open(server.SessionSpec{DSCP: 46, Timeout: 2 * time.Second})
		if c.opts.TOS != 46<<2 || c.opts.TTL != 255 || h.nets[0] != "udp4" || c.local != fullLocal {
			t.Errorf("socket %s %v opts %+v", h.nets[0], c.local, c.opts)
		}
		if out := send(c, senderPacket(1, 41), fullSender); len(out) != 0 {
			t.Error("reflected before Start")
		}
		s.Start(time.Now())
		for i := range uint32(3) {
			out := send(c, senderPacket(100+i, 41), fullSender)
			if len(out) != 1 {
				t.Fatalf("packet %d: %d replies", i, len(out))
			}
			var p wire.ReflectorPacket
			_ = p.Decode(out[0].b)
			if p.Seq != i || p.SenderSeq != 100+i || out[0].tos != -1 || out[0].to != fullSender {
				t.Errorf("reply %d: %+v tos %d to %v", i, p, out[0].tos, out[0].to)
			}
		}
		stopAt := time.Now().Add(2 * time.Second)
		s.Stop(stopAt)
		time.Sleep(2 * time.Second)
		if out := send(c, senderPacket(9, 41), fullSender); len(out) != 1 {
			t.Error("not reflected just at the end of Timeout")
		}
		time.Sleep(time.Nanosecond)
		if out := send(c, senderPacket(10, 41), fullSender); len(out) != 0 {
			t.Error("reflected after Timeout")
		}
		st := s.Stats()
		if st.Received != 4 || st.Reflected != 4 || !st.LastReceived.Equal(stopAt) {
			t.Errorf("stats = %+v", st)
		}
		cnt := h.full.Counters()
		if cnt.Received != 6 || cnt.Reflected != 4 || cnt.Dropped[reflector.DropNotStarted] != 1 || cnt.Dropped[reflector.DropDrained] != 1 {
			t.Errorf("counters = %+v", cnt)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.closed:
		default:
			t.Error("socket not closed")
		}
	})
}

// TestFullStartTime checks the Start Time of a request against the
// reflector's clock: by default the session reflects from Start-Sessions,
// as the Start Time is on the sender's clock (twping's runs 5 ms ahead
// where the kernel still slews an NTP offset, which dropped its first
// packets as not_started); with EnforceStartTime it waits for the Start Time.
func TestFullStartTime(t *testing.T) {
	t.Run("sender clock ahead", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newFull(t, reflector.FullConfig{})
			s, c := h.open(server.SessionSpec{StartTime: time.Now().Add(5 * time.Millisecond)})
			s.Start(time.Now())
			// The sender's first packet, sent at its Start Time by its clock.
			if out := send(c, senderPacket(1, 41), fullSender); len(out) != 1 {
				t.Error("first packet dropped before the Start Time by the reflector's clock")
			}
			if d := h.full.Counters().Dropped[reflector.DropNotStarted]; d != 0 {
				t.Errorf("not_started = %d", d)
			}
		})
	})
	t.Run("enforced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newFull(t, reflector.FullConfig{EnforceStartTime: true})
			s, c := h.open(server.SessionSpec{StartTime: time.Now().Add(time.Second)})
			s.Start(time.Now())
			if out := send(c, senderPacket(1, 41), fullSender); len(out) != 0 {
				t.Error("reflected before the Start Time")
			}
			time.Sleep(time.Second)
			if out := send(c, senderPacket(2, 41), fullSender); len(out) != 1 {
				t.Error("not reflected at the Start Time")
			}
		})
	})
	t.Run("before Start-Sessions", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := newFull(t, reflector.FullConfig{})
			_, c := h.open(server.SessionSpec{})
			if out := send(c, senderPacket(1, 41), fullSender); len(out) != 0 {
				t.Error("reflected before Start-Sessions")
			}
		})
	})
}

func TestFullVerifySender(t *testing.T) {
	samePortOther := netip.MustParseAddrPort("192.0.2.1:50001")
	otherAddr := netip.MustParseAddrPort("192.0.2.9:50000")
	mapped := netip.MustParseAddrPort("[::ffff:192.0.2.1]:50000")
	tests := []struct {
		name   string
		verify reflector.VerifySender
		from   []netip.AddrPort
		want   []bool
	}{
		{"address", reflector.VerifyAddress, []netip.AddrPort{fullSender, samePortOther, mapped, otherAddr}, []bool{true, true, true, false}},
		{"address_port", reflector.VerifyAddressPort, []netip.AddrPort{fullSender, samePortOther, mapped, otherAddr}, []bool{true, false, true, false}},
		{"none locks to the first source", reflector.VerifyNone, []netip.AddrPort{otherAddr, otherAddr, fullSender}, []bool{true, true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newFull(t, reflector.FullConfig{VerifySender: tt.verify})
				s, c := h.open(server.SessionSpec{})
				s.Start(time.Now())
				var dropped uint64
				for i, from := range tt.from {
					got := len(send(c, senderPacket(1, 41), from)) == 1
					if got != tt.want[i] {
						t.Errorf("packet %d from %v: reflected %v, want %v", i, from, got, tt.want[i])
					}
					if !tt.want[i] {
						dropped++
					}
				}
				if got := h.full.Counters().Dropped[reflector.DropUnknownSession]; got != dropped {
					t.Errorf("unknown_session drops = %d, want %d", got, dropped)
				}
			})
		})
	}
}

func TestFullMalformedAndErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{MaxPacketSize: 100})
		s, c := h.open(server.SessionSpec{})
		s.Start(time.Now())
		for _, n := range []int{13, 101} {
			if out := send(c, senderPacket(1, n), fullSender); len(out) != 0 {
				t.Errorf("%d octets reflected", n)
			}
		}
		if got := h.full.Counters().Dropped[reflector.DropMalformed]; got != 2 {
			t.Errorf("malformed drops = %d", got)
		}
		c.writeErrs = []error{syscall.EPERM}
		send(c, senderPacket(1, 41), fullSender)
		c.in <- rxPacket{err: errors.New("transient")}
		synctest.Wait()
		time.Sleep(time.Second)
		if out := send(c, senderPacket(2, 41), fullSender); len(out) != 1 {
			t.Error("session stopped after errors")
		}
		if cnt := h.full.Counters(); cnt.SendErrors != 1 || cnt.Reflected != 1 {
			t.Errorf("counters = %+v", cnt)
		}
		if st := s.Stats(); st.Received != 2 || st.Reflected != 1 {
			t.Errorf("stats = %+v", st)
		}
	})
}

func TestFullOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{Timestamping: tstamp.ModeUserspace, TTL: 64})
		h.open(server.SessionSpec{Local: netip.MustParseAddrPort("[2001:db8::2]:20000")})
		h.open(server.SessionSpec{Local: netip.MustParseAddrPort("[::ffff:198.51.100.2]:20001")})
		if h.nets[0] != "udp6" || h.nets[1] != "udp4" || h.conns[1].local != netip.MustParseAddrPort("198.51.100.2:20001") || h.conns[0].opts.TTL != 64 {
			t.Errorf("networks %v, local %v", h.nets, h.conns[1].local)
		}
		if s := h.full.Counters(); s.Received != 0 {
			t.Errorf("counters = %+v", s)
		}
	})
	inUse := func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return nil, syscall.EADDRINUSE }
	f, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{Listen: inUse})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Open(server.SessionSpec{Local: fullLocal}); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("Open error = %v, want EADDRINUSE kept", err)
	}
}

func TestFullConfig(t *testing.T) {
	for _, c := range []reflector.FullConfig{
		{VerifySender: 3}, {TTL: 300}, {MaxPacketSize: 13}, {KernelError: -1},
	} {
		if _, err := reflector.NewFull(c, reflector.Deps{}); err == nil {
			t.Errorf("NewFull(%+v) succeeded", c)
		}
	}
	d := reflector.FullConfig{}.WithDefaults()
	if d.TTL != 255 || d.MaxPacketSize != 9000 || d.VerifySender != reflector.VerifyAddress || d.UserspaceError != reflector.DefaultUserspaceError {
		t.Errorf("defaults = %+v", d)
	}
}

func TestFullDSCPECNMonitoring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{})
		s, c := h.open(server.SessionSpec{DSCPECNMonitoring: true})
		s.Start(time.Now())
		c.in <- rxPacket{b: senderPacket(1, 41), from: fullSender, meta: tstamp.RxMeta{At: time.Now(), TTL: 64, TOS: 10<<2 | 2}}
		synctest.Wait()
		out := c.sent()
		if len(out) != 1 || len(out[0].b) != 44 || out[0].b[41] != 10<<2|2 {
			t.Fatalf("replies %+v", out)
		}
	})
}

// TestFullRefusesPaddingAboveMaxPacket checks that a session whose packets
// would exceed max_packet_size is refused rather than accepted and lost
// (audit W5), and that the server answers it with Accept 3.
func TestFullRefusesPaddingAboveMaxPacket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{MaxPacketSize: 9000})
		if _, err := h.full.Open(server.SessionSpec{Local: fullLocal, Sender: fullSender, PaddingLength: 8987}); !errors.Is(err, server.ErrUnsupportedRequest) {
			t.Errorf("padding 8987 (9001-octet packets): err = %v", err)
		}
		if len(h.conns) != 0 {
			t.Error("a socket was opened for a refused session")
		}
		h.open(server.SessionSpec{PaddingLength: 8986}) // exactly 9000 octets
	})
}

// authSession returns the spec of a session in mode and a TestCrypto for the
// Session-Sender's side of it.
func authSession(t *testing.T, mode wire.SecurityMode, keyByte byte) (server.SessionSpec, *wire.TestCrypto) {
	t.Helper()
	sid := wire.SID{1, 2, 3, 4}
	keys := wire.DeriveTestKeys(wire.SessionKeys{AES: [16]byte{keyByte}, HMAC: [32]byte{keyByte + 1}}, sid)
	c, err := wire.NewTestCrypto(mode, keys)
	if err != nil {
		t.Fatal(err)
	}
	return server.SessionSpec{SID: sid, Security: mode, TestKeys: keys, Timeout: time.Second}, c
}

// authPacket is a sealed Session-Sender packet with padding octets of padding.
func authPacket(c *wire.TestCrypto, seq uint32, padding int) []byte {
	b := make([]byte, wire.AuthSenderHeaderSize+padding)
	p := wire.AuthSenderPacket{Seq: seq, Timestamp: ntptime.New(0x11223344, 0), ErrorEstimate: errest.New(true, false, 10, 210)}
	_ = p.PutHeader(b)
	for i := wire.AuthSenderHeaderSize; i < len(b); i++ {
		b[i] = byte(i)
	}
	_ = c.SealSender(b)
	return b
}

// TestFullAuthenticated reflects authenticated and encrypted packets and
// discards those that fail their HMAC without counting them for the session
// (RFC 4656 §4.2).
func TestFullAuthenticated(t *testing.T) {
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newFull(t, reflector.FullConfig{})
				spec, sc := authSession(t, mode, 7)
				_, forger := authSession(t, mode, 8) // the same SID, other control keys
				s, c := h.open(spec)
				s.Start(time.Now())

				out := send(c, authPacket(sc, 5, 100), fullSender)
				if len(out) != 1 || len(out[0].b) != 148 {
					t.Fatalf("replies %d, want one of 148 octets", len(out))
				}
				reply := out[0].b
				if err := sc.OpenReflector(reply); err != nil {
					t.Fatalf("reply: %v", err)
				}
				var p wire.AuthReflectorPacket
				_ = p.Decode(reply)
				if p.Seq != 0 || p.SenderSeq != 5 || p.SenderTimestamp != ntptime.New(0x11223344, 0) || p.SenderTTL != 64 || p.Timestamp.IsZero() || p.ReceiveTimestamp.IsZero() {
					t.Errorf("reply %+v", p)
				}
				if len(p.Padding) != 36 || p.Padding[0] != 48 {
					t.Errorf("padding %x, want the sender's from octet 48, truncated by 64", p.Padding)
				}

				before := s.Stats().Received
				for _, bad := range [][]byte{
					authPacket(forger, 6, 0),
					authPacket(sc, 7, 0)[:wire.AuthSenderHeaderSize-1],
					senderPacket(8, 41), // unauthenticated
				} {
					if out := send(c, bad, fullSender); len(out) != 0 {
						t.Errorf("reflected a packet that fails authentication: %x", bad)
					}
				}
				if st := s.Stats(); st.Received != before {
					t.Errorf("forged packets counted for the session: %+v", st)
				}
				if got := h.full.Counters().Dropped[reflector.DropMalformed]; got != 3 {
					t.Errorf("malformed drops = %d, want 3", got)
				}
				// The reflector numbers only what it reflects.
				out = send(c, authPacket(sc, 9, 64), fullSender)
				if len(out) != 1 || len(out[0].b) != 112 || sc.OpenReflector(out[0].b) != nil {
					t.Fatalf("second reply: %d", len(out))
				}
				_ = p.Decode(out[0].b)
				if p.Seq != 1 || p.SenderSeq != 9 {
					t.Errorf("second reply %+v", p)
				}
			})
		})
	}
}

// TestFullAuthenticatedVerifyNone checks that a forged packet cannot fix the
// source a VerifyNone session follows.
func TestFullAuthenticatedVerifyNone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{VerifySender: reflector.VerifyNone})
		spec, sc := authSession(t, wire.SecurityEncrypted, 7)
		_, forger := authSession(t, wire.SecurityEncrypted, 8)
		s, c := h.open(spec)
		s.Start(time.Now())
		attacker := netip.MustParseAddrPort("203.0.113.9:4000")
		if out := send(c, authPacket(forger, 0, 0), attacker); len(out) != 0 {
			t.Fatal("forged packet reflected")
		}
		if out := send(c, authPacket(sc, 0, 0), fullSender); len(out) != 1 {
			t.Error("the real sender was locked out by a forged packet")
		}
	})
}

func TestFullAuthenticatedOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{MaxPacketSize: 9000})
		spec, _ := authSession(t, wire.SecurityAuthenticated, 1)
		spec.Local, spec.Sender = fullLocal, fullSender
		spec.PaddingLength = 8953 // 48 + 8953 = 9001 octets
		if _, err := h.full.Open(spec); !errors.Is(err, server.ErrUnsupportedRequest) {
			t.Errorf("padding 8953: err = %v", err)
		}
		spec.PaddingLength = 8952
		h.open(spec)
		// A mixed-mode session sends unauthenticated test packets.
		mixed := server.SessionSpec{Security: wire.SecurityMixed}
		s, c := h.open(mixed)
		s.Start(time.Now())
		if out := send(c, senderPacket(1, 41), fullSender); len(out) != 1 || len(out[0].b) != 41 {
			t.Errorf("mixed session: %d replies", len(out))
		}
	})
}

// TestFullSymmetricalSize reflects under RFC 6038 Symmetrical Size: the
// padding stays in place and the reply is as long as the request, the
// reflected octets first (Reflect Octets).
func TestFullSymmetricalSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{})
		layout := wire.PaddingLayout{SymmetricalSize: true, ReflectLength: 4}
		s, c := h.open(server.SessionSpec{Layout: layout, PaddingLength: 8})
		s.Start(time.Now())
		in := senderPacket(3, 41+8)
		clear(in[14:41]) // the MBZ octets
		copy(in[41:], []byte{0xbe, 0xef, 1, 2, 5, 6, 7, 8})
		out := send(c, in, fullSender)
		if len(out) != 1 || len(out[0].b) != 49 || !bytes.Equal(out[0].b[41:], in[41:]) {
			t.Fatalf("replies %d: %+v", len(out), out)
		}
		// A packet shorter than the symmetrical header is malformed.
		if out := send(c, senderPacket(4, 40), fullSender); len(out) != 0 {
			t.Error("reflected a 40-octet packet")
		}
	})
}

// TestFullSymmetricalSizeAuthenticated does the same with the 64 MBZ
// octets and 112-octet reply header of the authenticated modes (EID 5549).
func TestFullSymmetricalSizeAuthenticated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newFull(t, reflector.FullConfig{})
		spec, sc := authSession(t, wire.SecurityEncrypted, 7)
		spec.Layout = wire.PaddingLayout{SymmetricalSize: true}
		spec.PaddingLength = 8
		s, c := h.open(spec)
		s.Start(time.Now())
		in := authPacket(sc, 1, 64+8) // 48-octet header, 64 MBZ, 8 octets of padding
		out := send(c, in, fullSender)
		if len(out) != 1 || len(out[0].b) != 120 || !bytes.Equal(out[0].b[112:], in[112:]) {
			t.Fatalf("reply %d octets", len(out[0].b))
		}
		if err := sc.OpenReflector(out[0].b); err != nil {
			t.Error(err)
		}
		spec.PaddingLength = 9000 - 112 + 1
		if _, err := h.full.Open(spec); !errors.Is(err, server.ErrUnsupportedRequest) {
			t.Errorf("padding past max_packet_size with the symmetrical header: %v", err)
		}
	})
}
