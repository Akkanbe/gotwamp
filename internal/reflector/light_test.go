package reflector_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// rxPacket is a datagram the fake socket hands to ReadFrom, or an error.
//
//declscope:package // shared by the Light and Full tests
type rxPacket struct {
	b    []byte
	from netip.AddrPort
	meta tstamp.RxMeta
	err  error
}

// txPacket is a datagram written through the fake socket.
//
//declscope:package // shared by the Light and Full tests
type txPacket struct {
	b   []byte
	to  netip.AddrPort
	tos int
	// src is the source address given to WriteToFrom; zero for WriteTo.
	src netip.Addr
	//declscope:private
	at time.Time
}

// fakeConn is an in-memory tstamp.Conn. ReadFrom blocks on a channel, so
// inside a synctest bubble synctest.Wait returns once every queued packet
// has been handled.
//
//declscope:package // shared by the Light and Full tests
type fakeConn struct {
	local  netip.AddrPort
	opts   tstamp.Options
	mode   tstamp.Mode
	in     chan rxPacket
	closed chan struct{}
	//declscope:private
	once sync.Once

	//declscope:private
	mu sync.Mutex
	//declscope:private
	out []txPacket
	//declscope:private
	writes    int
	writeErrs []error // returned by successive WriteTo calls before succeeding
}

func (c *fakeConn) ReadFrom(b []byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	select {
	case p := <-c.in:
		if p.err != nil {
			return 0, netip.AddrPort{}, tstamp.RxMeta{}, p.err
		}
		return copy(b, p.b), p.from, p.meta, nil // truncates like recvmsg
	case <-c.closed:
		return 0, netip.AddrPort{}, tstamp.RxMeta{}, net.ErrClosed
	}
}

func (c *fakeConn) WriteTo(b []byte, to netip.AddrPort, tos int) (time.Time, error) {
	return c.WriteToFrom(b, to, netip.Addr{}, tos)
}

// WriteToFrom implements tstamp.SourceWriter.
func (c *fakeConn) WriteToFrom(b []byte, to netip.AddrPort, from netip.Addr, tos int) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes++
	if len(c.writeErrs) > 0 {
		err := c.writeErrs[0]
		c.writeErrs = c.writeErrs[1:]
		return time.Time{}, err
	}
	now := time.Now()
	c.out = append(c.out, txPacket{b: bytes.Clone(b), to: to, tos: tos, src: from, at: now})
	return now, nil
}

func (c *fakeConn) LocalAddr() netip.AddrPort       { return c.local }
func (c *fakeConn) SetReadDeadline(time.Time) error { return nil }
func (c *fakeConn) Mode() tstamp.Mode               { return c.mode }

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// sent returns and forgets the datagrams written so far.
//
//declscope:package // shared by the Light and Full tests
func (c *fakeConn) sent() []txPacket {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.out
	c.out = nil
	return out
}

// harness runs a Light reflector over fake sockets inside a synctest bubble.
type harness struct {
	t     *testing.T
	light *reflector.Light
	conns []*fakeConn
	nets  []string
}

var (
	listenerA = netip.MustParseAddrPort("192.0.2.10:862")
	listenerB = netip.MustParseAddrPort("[2001:db8::10]:862")
	senderA   = netip.MustParseAddrPort("192.0.2.1:50000")
	senderB   = netip.MustParseAddrPort("192.0.2.2:50001")
)

// start must run inside synctest.Test. Unless cfg names listeners, it
// listens on listenerA only.
func start(t *testing.T, cfg reflector.Config, deps reflector.Deps) *harness {
	t.Helper()
	h := &harness{t: t}
	if cfg.Listeners == nil {
		cfg.Listeners = []netip.AddrPort{listenerA}
	}
	deps.Listen = func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
		c := &fakeConn{local: local, opts: opts, mode: tstamp.ModeUserspace, in: make(chan rxPacket, 64), closed: make(chan struct{})}
		if opts.Mode == tstamp.ModeKernel {
			c.mode = tstamp.ModeKernel
		}
		h.conns = append(h.conns, c)
		h.nets = append(h.nets, network)
		return c, nil
	}
	l, err := reflector.ListenLight(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	h.light = l
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- l.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return h
}

// senderPacket builds a Session-Sender packet of n octets whose padding
// counts up from 0.
//
//declscope:package // shared by the Light and Full tests
func senderPacket(seq uint32, n int) []byte {
	p := wire.SenderPacket{Seq: seq, Timestamp: ntptime.New(0x11223344, 0x55667788), ErrorEstimate: errest.New(true, false, 10, 210)}
	b := make([]byte, max(n, wire.SenderHeaderSize))
	_ = p.PutHeader(b)
	for i := wire.SenderHeaderSize; i < len(b); i++ {
		b[i] = byte(i & 0xff)
	}
	return b[:n]
}

// send queues a datagram on listener i, waits until it is handled and
// returns the replies written.
func (h *harness) send(i int, b []byte, from netip.AddrPort, meta tstamp.RxMeta) []txPacket {
	h.t.Helper()
	if meta.At.IsZero() {
		meta.At = time.Now()
	}
	h.conns[i].in <- rxPacket{b: b, from: from, meta: meta}
	synctest.Wait()
	return h.conns[i].sent()
}

// reflect sends a packet expected to be answered and decodes the reply.
func (h *harness) reflect(i int, b []byte, from netip.AddrPort, meta tstamp.RxMeta) (wire.ReflectorPacket, txPacket) {
	h.t.Helper()
	out := h.send(i, b, from, meta)
	if len(out) != 1 {
		h.t.Fatalf("got %d replies, want 1", len(out))
	}
	var p wire.ReflectorPacket
	if err := p.Decode(out[0].b); err != nil {
		h.t.Fatal(err)
	}
	return p, out[0]
}

// eventually waits up to 5 s for cond, for counters that real sockets
// update asynchronously.
//
//declscope:package // used by the loopback tests
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) dropped(r reflector.DropReason) uint64 {
	return h.light.Status().Counters.Dropped[r]
}

func TestReflectLengths(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantLen int // 0 means dropped as malformed
	}{
		{"empty", 0, 0},
		{"below sender header", 13, 0},
		{"sender header only", 14, 41},
		{"padding below truncation", 30, 41},
		{"default symmetric size", 41, 41},
		{"STAMP base packet", 44, 44},
		{"IPv4 MTU", 1472, 1472},
		{"max packet size", 9000, 9000},
		{"above max packet size", 9001, 0},
		{"far above max packet size", 20000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := start(t, reflector.Config{}, reflector.Deps{})
				in := senderPacket(7, tt.n)
				out := h.send(0, in, senderA, tstamp.RxMeta{TTL: 60, TOS: -1})
				if tt.wantLen == 0 {
					if len(out) != 0 || h.dropped(reflector.DropMalformed) != 1 {
						t.Fatalf("replies %d, malformed drops %d; want 0 and 1", len(out), h.dropped(reflector.DropMalformed))
					}
					return
				}
				if len(out) != 1 || len(out[0].b) != tt.wantLen {
					t.Fatalf("replies %v, want one of %d octets", len(out), tt.wantLen)
				}
				var p wire.ReflectorPacket
				if err := p.Decode(out[0].b); err != nil {
					t.Fatal(err)
				}
				// Padding is the sender's, with the highest-numbered octets dropped.
				if want := in[wire.SenderHeaderSize:][:tt.wantLen-wire.ReflectorHeaderSize]; !bytes.Equal(p.Padding, want) {
					t.Errorf("padding %x, want %x", p.Padding, want)
				}
				if out[0].to != senderA {
					t.Errorf("reply sent to %v, want %v", out[0].to, senderA)
				}
			})
		})
	}
}

// TestSTAMPBasePacket checks SPEC §6.7: a 44-octet STAMP sender packet gets a
// 44-octet reply carrying the first 3 octets of its MBZ/SSID area at 41–43.
func TestSTAMPBasePacket(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{}, reflector.Deps{})
		in := make([]byte, 44)
		copy(in, senderPacket(1, 14))
		in[14], in[15] = 0xab, 0xcd // RFC 8972 SSID
		_, tx := h.reflect(0, in, senderA, tstamp.RxMeta{TTL: 255, TOS: 0})
		if len(tx.b) != 44 || !bytes.Equal(tx.b[41:44], []byte{0xab, 0xcd, 0}) {
			t.Errorf("reply %x", tx.b)
		}
	})
}

func TestReplyFields(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{}, reflector.Deps{})
		t2 := time.Now().Add(-3 * time.Millisecond)
		in := senderPacket(42, 41)
		p, tx := h.reflect(0, in, senderA, tstamp.RxMeta{At: t2, TTL: 61, TOS: -1})
		var s wire.SenderPacket
		_ = s.Decode(in)
		if p.SenderSeq != 42 || p.SenderTimestamp != s.Timestamp || p.SenderErrorEstimate != s.ErrorEstimate {
			t.Errorf("sender fields not copied: %+v", p)
		}
		if p.ReceiveTimestamp != ntptime.FromTime(t2) {
			t.Errorf("T2 = %v, want %v", p.ReceiveTimestamp.Time(t2), t2)
		}
		if p.Timestamp != ntptime.FromTime(tx.at) {
			t.Errorf("T3 = %v, want the send time %v", p.Timestamp.Time(t2), tx.at)
		}
		if p.SenderTTL != 61 {
			t.Errorf("Sender TTL = %d, want 61", p.SenderTTL)
		}
		if p.ErrorEstimate != errest.Unknown {
			t.Errorf("Error Estimate without a clock = %v, want unknown", p.ErrorEstimate)
		}
		if tx.b[14] != 0 || tx.b[15] != 0 || tx.b[38] != 0 || tx.b[39] != 0 {
			t.Errorf("MBZ not zero: %x", tx.b)
		}

		// An unreadable TTL reflects as 255 (RFC 5357 §4.2.1).
		p, _ = h.reflect(0, senderPacket(43, 41), senderA, tstamp.RxMeta{TTL: -1, TOS: -1})
		if p.SenderTTL != 255 {
			t.Errorf("unknown Sender TTL = %d, want 255", p.SenderTTL)
		}
		// A missing receive time falls back to the read time.
		p, _ = h.reflect(0, senderPacket(44, 41), senderA, tstamp.RxMeta{At: time.Time{}, TTL: 1, TOS: -1})
		if p.ReceiveTimestamp != ntptime.FromTime(time.Now()) {
			t.Errorf("T2 fallback = %v", p.ReceiveTimestamp)
		}
	})
}

func TestDSCPPolicy(t *testing.T) {
	t.Run("copy", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, reflector.Config{}, reflector.Deps{})
			if h.conns[0].opts.TOS != 0 {
				t.Errorf("socket TOS = %#x, want unset", h.conns[0].opts.TOS)
			}
			for _, tt := range []struct{ rx, want int }{
				{46<<2 | 1, 46 << 2}, // ECN cleared to Not-ECT
				{0x20, 0x20},
				{0, 0},
				{-1, 0}, // unknown: CS0
			} {
				_, tx := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: tt.rx})
				if tx.tos != tt.want {
					t.Errorf("received TOS %#x: reply TOS %#x, want %#x", tt.rx, tx.tos, tt.want)
				}
			}
		})
	})
	t.Run("fixed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, reflector.Config{DSCPPolicy: reflector.DSCPFixed, DSCPFixed: 34}, reflector.Deps{})
			if h.conns[0].opts.TOS != 34<<2 {
				t.Errorf("socket TOS = %#x, want %#x", h.conns[0].opts.TOS, 34<<2)
			}
			_, tx := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: 46 << 2})
			if tx.tos != -1 {
				t.Errorf("reply TOS %d, want the socket default (-1)", tx.tos)
			}
		})
	})
}

func TestSequencePerFlow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{Listeners: []netip.AddrPort{listenerA, listenerB}}, reflector.Deps{})
		senderV6 := netip.MustParseAddrPort("[2001:db8::1]:50000")
		steps := []struct {
			listener int
			from     netip.AddrPort
			want     uint32
		}{
			{0, senderA, 0},
			{0, senderA, 1},
			{0, senderB, 0},
			{0, senderA, 2},
			{1, senderV6, 0},
			{0, senderB, 1},
		}
		for i, s := range steps {
			p, _ := h.reflect(s.listener, senderPacket(1000+uint32(i), 41), s.from, tstamp.RxMeta{TTL: 64, TOS: -1})
			if p.Seq != s.want {
				t.Errorf("step %d from %v: seq %d, want %d", i, s.from, p.Seq, s.want)
			}
		}
		flows := h.light.Flows()
		slices.SortFunc(flows, func(a, b reflector.Flow) int { return a.Source.Compare(b.Source) })
		if len(flows) != 3 {
			t.Fatalf("flows = %+v", flows)
		}
		a := flows[0]
		if a.Source != senderA || a.Destination != listenerA || a.NextSeq != 3 || a.Received != 3 || a.Reflected != 3 || !a.FirstReceived.Equal(time.Now()) || !a.LastReceived.Equal(time.Now()) {
			t.Errorf("flow A = %+v", a)
		}
		if st := h.light.Status(); st.FlowsActive != 3 || st.Counters.Received != 6 || st.Counters.Reflected != 6 ||
			st.Listeners[0].Counters.Reflected != 5 || st.Listeners[1].Counters.Received != 1 {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestSequenceCopySender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{Sequence: reflector.SequenceCopySender}, reflector.Deps{})
		for _, seq := range []uint32{5, 9, 9, 0xffffffff} {
			p, _ := h.reflect(0, senderPacket(seq, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1})
			if p.Seq != seq {
				t.Errorf("seq %d, want the sender's %d", p.Seq, seq)
			}
		}
		if f := h.light.Flows(); len(f) != 1 || f[0].Received != 4 || f[0].NextSeq != 0 {
			t.Errorf("flows = %+v", f)
		}
	})
}

func TestFlowExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{FlowTimeout: 30 * time.Second}, reflector.Deps{})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		h.reflect(0, senderPacket(0, 41), senderA, meta)
		h.reflect(0, senderPacket(1, 41), senderA, meta)

		// Still alive just at the timeout.
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if p, _ := h.reflect(0, senderPacket(2, 41), senderA, meta); p.Seq != 2 {
			t.Errorf("seq after 30 s idle = %d, want 2", p.Seq)
		}

		// The sweeper forgets the flow once it has been idle longer.
		time.Sleep(35 * time.Second)
		synctest.Wait()
		if f := h.light.Flows(); len(f) != 0 {
			t.Errorf("flows after expiry = %+v", f)
		}
		if st := h.light.Status(); st.FlowsActive != 0 {
			t.Errorf("FlowsActive = %d, want 0", st.FlowsActive)
		}
		if p, _ := h.reflect(0, senderPacket(3, 41), senderA, meta); p.Seq != 0 {
			t.Errorf("seq of the restarted flow = %d, want 0", p.Seq)
		}
	})
}

// TestFlowExpiryBeforeSweep checks that a packet arriving after the timeout
// restarts the flow even before the sweeper has run.
func TestFlowExpiryBeforeSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{FlowTimeout: time.Hour}, reflector.Deps{})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		h.reflect(0, senderPacket(0, 41), senderA, meta)
		// The sweep runs every minute; arrive between two sweeps.
		meta.At = time.Now().Add(time.Hour + time.Second)
		if p, _ := h.reflect(0, senderPacket(1, 41), senderA, meta); p.Seq != 0 {
			t.Errorf("seq = %d, want 0 after the flow timed out", p.Seq)
		}
	})
}

func TestMaxFlows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{MaxFlows: 2}, reflector.Deps{})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		h.reflect(0, senderPacket(10, 41), senderA, meta)
		h.reflect(0, senderPacket(10, 41), senderB, meta)
		third := netip.MustParseAddrPort("192.0.2.3:50002")
		for _, seq := range []uint32{77, 78} {
			if p, _ := h.reflect(0, senderPacket(seq, 41), third, meta); p.Seq != seq {
				t.Errorf("stateless reply seq = %d, want the sender's %d", p.Seq, seq)
			}
		}
		// Existing flows keep their state.
		if p, _ := h.reflect(0, senderPacket(11, 41), senderA, meta); p.Seq != 1 {
			t.Errorf("existing flow seq = %d, want 1", p.Seq)
		}
		st := h.light.Status()
		if st.FlowsActive != 2 || st.FlowsOverflow != 2 || st.Counters.Reflected != 5 {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestACL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{
			Listeners: []netip.AddrPort{listenerA, listenerB},
			ACL: []netip.Prefix{
				netip.MustParsePrefix("192.0.2.0/30"),
				netip.MustParsePrefix("::ffff:198.51.100.0/120"), // IPv4-mapped form of 198.51.100.0/24
				netip.MustParsePrefix("2001:db8:1::/48"),
			},
		}, reflector.Deps{})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		tests := []struct {
			listener int
			from     string
			allowed  bool
		}{
			{0, "192.0.2.1:1", true},
			{0, "192.0.2.4:1", false},
			{0, "198.51.100.200:1", true},
			{1, "[::ffff:192.0.2.2]:1", true}, // mapped source matches the IPv4 prefix
			{1, "[2001:db8:1::5]:1", true},
			{1, "[2001:db8:2::5]:1", false},
			{0, "203.0.113.1:1", false},
		}
		var drops uint64
		for _, tt := range tests {
			out := h.send(tt.listener, senderPacket(1, 41), netip.MustParseAddrPort(tt.from), meta)
			if got := len(out) == 1; got != tt.allowed {
				t.Errorf("%s: reflected %v, want %v", tt.from, got, tt.allowed)
			}
			if !tt.allowed {
				drops++
			}
		}
		if got := h.dropped(reflector.DropACL); got != drops {
			t.Errorf("acl drops = %d, want %d", got, drops)
		}
		if f := h.light.Flows(); len(f) != 4 {
			t.Errorf("dropped packets created flows: %+v", f)
		}
	})
}

func TestRateLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{RateLimitPPS: 3}, reflector.Deps{})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		burst := func(from netip.AddrPort, n int) (reflected int) {
			for range n {
				reflected += len(h.send(0, senderPacket(1, 41), from, meta))
			}
			return reflected
		}
		if got := burst(senderA, 5); got != 3 {
			t.Errorf("burst of 5 at 3 pps: %d reflected, want 3", got)
		}
		// Another port of the same address shares the bucket; another address does not.
		if got := burst(netip.AddrPortFrom(senderA.Addr(), 1234), 1); got != 0 {
			t.Errorf("same address, other port: %d reflected, want 0", got)
		}
		if got := burst(senderB, 3); got != 3 {
			t.Errorf("other source: %d reflected, want 3", got)
		}
		if got := h.dropped(reflector.DropRateLimit); got != 3 {
			t.Errorf("rate_limit drops = %d, want 3", got)
		}
		time.Sleep(time.Second / 2) // 1.5 tokens
		if got := burst(senderA, 2); got != 1 {
			t.Errorf("after 1/2 s: %d reflected, want 1", got)
		}
		// Idle sources are swept; a returning one gets a full bucket.
		time.Sleep(2 * time.Minute)
		if got := burst(senderA, 4); got != 3 {
			t.Errorf("after idling: %d reflected, want 3", got)
		}
	})
}

func TestSendRetry(t *testing.T) {
	tests := []struct {
		name      string
		errs      []error
		writes    int
		reflected uint64
	}{
		{"ENOBUFS once", []error{syscall.ENOBUFS}, 2, 1},
		{"EAGAIN once", []error{&net.OpError{Op: "write", Err: os.NewSyscallError("sendmsg", syscall.EAGAIN)}}, 2, 1},
		{"ENOBUFS twice", []error{syscall.ENOBUFS, syscall.ENOBUFS}, 2, 0},
		{"not retryable", []error{syscall.EPERM}, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := start(t, reflector.Config{}, reflector.Deps{})
				c := h.conns[0]
				c.writeErrs = tt.errs
				h.send(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1})
				st := h.light.Status()
				if c.writes != tt.writes || st.Counters.Reflected != tt.reflected || st.Counters.SendErrors != 1-tt.reflected {
					t.Errorf("writes %d, reflected %d, send errors %d", c.writes, st.Counters.Reflected, st.Counters.SendErrors)
				}
				if f := h.light.Flows(); len(f) != 1 || f[0].Reflected != tt.reflected || f[0].Received != 1 {
					t.Errorf("flow = %+v", f)
				}
			})
		})
	}
}

func TestErrorEstimate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		clock := func() (bool, time.Duration) {
			calls++
			return true, time.Millisecond
		}
		h := start(t, reflector.Config{UserspaceError: 20 * time.Microsecond}, reflector.Deps{Clock: clock})
		meta := tstamp.RxMeta{TTL: 64, TOS: -1}
		want := errest.Compute(time.Millisecond+20*time.Microsecond, true)
		for range 3 {
			if p, _ := h.reflect(0, senderPacket(1, 41), senderA, meta); p.ErrorEstimate != want {
				t.Errorf("Error Estimate %v, want %v", p.ErrorEstimate, want)
			}
		}
		if calls != 1 {
			t.Errorf("clock read %d times within a second, want 1", calls)
		}
		time.Sleep(time.Second)
		h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1})
		if calls != 2 {
			t.Errorf("clock read %d times after a second, want 2", calls)
		}
	})
}

func TestErrorEstimateKernel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clock := func() (bool, time.Duration) { return false, 0 }
		h := start(t, reflector.Config{Timestamping: tstamp.ModeKernel}, reflector.Deps{Clock: clock})
		p, _ := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1})
		// T3 is a user-space timestamp even with kernel receive timestamps.
		if want := errest.Compute(reflector.DefaultUserspaceError, false); p.ErrorEstimate != want {
			t.Errorf("Error Estimate %v, want %v", p.ErrorEstimate, want)
		}
		if st := h.light.Status(); st.Listeners[0].Timestamping != tstamp.ModeKernel {
			t.Errorf("timestamping = %v", st.Listeners[0].Timestamping)
		}
	})
}

func TestReadErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{}, reflector.Deps{})
		h.conns[0].in <- rxPacket{err: os.ErrDeadlineExceeded}
		h.conns[0].in <- rxPacket{err: errors.New("transient")}
		synctest.Wait()
		time.Sleep(time.Second)
		if _, tx := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1}); len(tx.b) != 41 {
			t.Errorf("listener stopped after read errors")
		}
	})
}

func TestListenDefaults(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{Listeners: []netip.AddrPort{}}, reflector.Deps{})
		if !slices.Equal(h.light.Addrs(), reflector.DefaultListeners) {
			t.Errorf("Addrs() = %v, want %v", h.light.Addrs(), reflector.DefaultListeners)
		}
		if !slices.Equal(h.nets, []string{"udp6", "udp4"}) {
			t.Errorf("networks = %v", h.nets)
		}
		if o := h.conns[0].opts; o.TTL != 255 || o.TOS != 0 || o.Mode != tstamp.ModeAuto {
			t.Errorf("socket options = %+v", o)
		}
	})
}

func TestListenOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mapped := netip.MustParseAddrPort("[::ffff:127.0.0.1]:8620")
		h := start(t, reflector.Config{Listeners: []netip.AddrPort{mapped}, TTL: 64, ReadBuffer: 4096}, reflector.Deps{})
		if h.nets[0] != "udp4" || h.conns[0].local != netip.MustParseAddrPort("127.0.0.1:8620") {
			t.Errorf("mapped listener opened as %s %v", h.nets[0], h.conns[0].local)
		}
		if o := h.conns[0].opts; o.TTL != 64 || o.ReadBuffer != 4096 {
			t.Errorf("socket options = %+v", o)
		}
	})
}

func TestListenFailureClosesOpened(t *testing.T) {
	var opened []*fakeConn
	listen := func(_ string, local netip.AddrPort, _ tstamp.Options) (tstamp.Conn, error) {
		if local == listenerB {
			return nil, syscall.EADDRINUSE
		}
		c := &fakeConn{local: local, in: make(chan rxPacket), closed: make(chan struct{})}
		opened = append(opened, c)
		return c, nil
	}
	_, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{listenerA, listenerB}}, reflector.Deps{Listen: listen})
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("err = %v, want EADDRINUSE", err)
	}
	select {
	case <-opened[0].closed:
	default:
		t.Error("the first listener was left open")
	}
}

func TestServeTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{}, reflector.Deps{})
		synctest.Wait()
		if err := h.light.Serve(t.Context()); err == nil {
			t.Error("second Serve succeeded")
		}
	})
}

func TestConfigValidation(t *testing.T) {
	bad := []reflector.Config{
		{Listeners: []netip.AddrPort{{}}},
		{Listeners: []netip.AddrPort{listenerA, listenerA}},
		{ACL: []netip.Prefix{{}}},
		{Sequence: 2},
		{FlowTimeout: -time.Second},
		{MaxFlows: -1},
		{RateLimitPPS: -1},
		{DSCPPolicy: 2},
		{DSCPFixed: 64},
		{TTL: 256},
		{TTL: -1},
		{MaxPacketSize: 13},
		{MaxPacketSize: 65508},
		{UserspaceError: -1},
	}
	for _, c := range bad {
		if err := c.WithDefaults().Validate(); err == nil {
			t.Errorf("%+v validated", c)
		}
		if _, err := reflector.ListenLight(c, reflector.Deps{Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) {
			t.Fatal("listened despite an invalid config")
			return nil, nil
		}}); err == nil {
			t.Errorf("ListenLight(%+v) succeeded", c)
		}
	}
	if err := (reflector.Config{}).WithDefaults().Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
	d := reflector.Config{}.WithDefaults()
	if d.FlowTimeout != 900*time.Second || d.MaxFlows != 65536 || d.TTL != 255 || d.MaxPacketSize != 9000 ||
		d.UserspaceError != 50*time.Microsecond || d.KernelError != 5*time.Microsecond || len(d.Listeners) != 2 {
		t.Errorf("defaults = %+v", d)
	}
}

func TestDropReasonNames(t *testing.T) {
	var names []string
	for _, r := range reflector.DropReasons() {
		names = append(names, r.String())
	}
	if !slices.Equal(names, []string{"acl", "malformed", "unknown_session", "rate_limit", "not_started", "drained"}) {
		t.Errorf("names = %v", names)
	}
	if reflector.DropReason(200).String() != "unknown" {
		t.Error("unknown reason name")
	}
}

// stampHook acts like a STAMP extension: it reflects the SSID (offsets
// 14–15 of the request, RFC 8972) and appends a 4-octet TLV.
type stampHook struct {
	seen []reflector.HookPacket
	drop bool
	grow int
}

func (h *stampHook) Reply(in *reflector.HookPacket, reply []byte) (int, bool) {
	h.seen = append(h.seen, *in)
	if h.drop {
		return 0, false
	}
	copy(reply[14:16], in.Head[14:16])
	n := len(reply)
	tlv := reply[n : n+4] // within the buffer's capacity
	copy(tlv, []byte{0x80, 0x01, 0x00, 0x00})
	return n + 4 + h.grow, true
}

func TestLightHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hook := &stampHook{}
		h := start(t, reflector.Config{}, reflector.Deps{Hook: hook})
		in := senderPacket(5, 44)
		in[14], in[15] = 0x12, 0x34 // SSID
		out := h.send(0, in, senderA, tstamp.RxMeta{TTL: 64, TOS: 0x20})
		if len(out) != 1 {
			t.Fatalf("%d replies", len(out))
		}
		r := out[0].b
		if len(r) != 48 || r[14] != 0x12 || r[15] != 0x34 || !bytes.Equal(r[44:], []byte{0x80, 0x01, 0, 0}) {
			t.Errorf("reply %x", r)
		}
		var p wire.ReflectorPacket
		_ = p.Decode(r)
		if p.SenderSeq != 5 || p.Timestamp.IsZero() {
			t.Errorf("reply fields %+v (T3 must be written after the hook)", p)
		}
		s := hook.seen[0]
		if s.Len != 44 || len(s.Head) != 44 || s.Head[14] != 0x12 || s.From != senderA || s.To != listenerA || s.RX.TOS != 0x20 {
			t.Errorf("hook saw %+v", s)
		}

		// A short request gives the hook only what arrived.
		h.send(0, senderPacket(6, 20), senderA, tstamp.RxMeta{TTL: 64, TOS: -1})
		if s := hook.seen[1]; s.Len != 20 || len(s.Head) != 20 {
			t.Errorf("hook saw %d octets of a %d-octet packet", len(s.Head), s.Len)
		}

		hook.drop = true
		if out := h.send(0, senderPacket(7, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1}); len(out) != 0 {
			t.Error("reflected a packet the hook dropped")
		}
		hook.drop, hook.grow = false, 1<<20
		if out := h.send(0, senderPacket(8, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1}); len(out) != 0 {
			t.Error("reflected a reply longer than the buffer")
		}
		if got := h.dropped(reflector.DropMalformed); got != 2 {
			t.Errorf("malformed drops = %d, want 2", got)
		}
	})
}

// TestLightDSCPECNMonitoring checks the RFC 7750 layout of Light replies.
func TestLightDSCPECNMonitoring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{DSCPECNMonitoring: true}, reflector.Deps{})
		for _, tt := range []struct {
			n, tos, wantLen int
			want            wire.DSCPECN
		}{
			{41, 46<<2 | 1, 44, wire.NewDSCPECN(46, 1)}, // the reply grows to the 44-octet header
			{14, -1, 44, 0}, // TOS unknown
			{100, 0x20, 100, 0x20},
		} {
			_, tx := h.reflect(0, senderPacket(1, tt.n), senderA, tstamp.RxMeta{TTL: 64, TOS: tt.tos})
			p := wire.ReflectorPacket{HasDSCPECN: true}
			if err := p.Decode(tx.b); err != nil {
				t.Fatal(err)
			}
			if len(tx.b) != tt.wantLen || p.DSCPECN != tt.want || tx.b[42] != 0 || tx.b[43] != 0 {
				t.Errorf("n=%d tos=%#x: %d octets, S-DSCP-ECN %#x", tt.n, tt.tos, len(tx.b), uint8(p.DSCPECN))
			}
			if tt.n == 100 && !bytes.Equal(p.Padding, senderPacket(1, 100)[14:14+56]) {
				t.Errorf("padding %x", p.Padding)
			}
		}
	})
}

// TestWildcardListenerRepliesFromDestination checks SPEC §6.1 / §6.6 on a
// listener bound to the wildcard: the reply leaves from the address the
// packet was sent to, and each local address keys its own flow (audit W1).
func TestWildcardListenerRepliesFromDestination(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wild := netip.MustParseAddrPort("0.0.0.0:862")
		h := start(t, reflector.Config{Listeners: []netip.AddrPort{wild}}, reflector.Deps{})
		vip := netip.MustParseAddr("192.0.2.10")
		other := netip.MustParseAddr("192.0.2.11")

		p, tx := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1, Dst: vip})
		if tx.src != vip || p.Seq != 0 {
			t.Errorf("reply from %v seq %d, want from %v seq 0", tx.src, p.Seq, vip)
		}
		// The same sender to another local address is another flow.
		p, tx = h.reflect(0, senderPacket(2, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1, Dst: other})
		if tx.src != other || p.Seq != 0 {
			t.Errorf("reply from %v seq %d, want from %v seq 0", tx.src, p.Seq, other)
		}
		p, _ = h.reflect(0, senderPacket(3, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1, Dst: vip})
		if p.Seq != 1 {
			t.Errorf("second packet to %v: seq %d, want 1", vip, p.Seq)
		}
		flows := h.light.Flows()
		slices.SortFunc(flows, func(a, b reflector.Flow) int { return a.Destination.Compare(b.Destination) })
		if len(flows) != 2 || flows[0].Destination != netip.AddrPortFrom(vip, 862) || flows[1].Destination != netip.AddrPortFrom(other, 862) {
			t.Errorf("flows = %+v", flows)
		}
		// Without the destination (an older kernel), the route chooses.
		if _, tx := h.reflect(0, senderPacket(4, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1}); tx.src.IsValid() {
			t.Errorf("reply source %v set without a destination", tx.src)
		}
	})
}

// TestBoundListenerKeepsItsAddress checks that a listener on a specific
// address neither sets the source nor keys flows by Dst.
func TestBoundListenerKeepsItsAddress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{}, reflector.Deps{}) // listenerA, a specific address
		_, tx := h.reflect(0, senderPacket(1, 41), senderA, tstamp.RxMeta{TTL: 64, TOS: -1, Dst: netip.MustParseAddr("192.0.2.99")})
		if tx.src.IsValid() {
			t.Errorf("reply source %v set on a bound listener", tx.src)
		}
		if f := h.light.Flows(); len(f) != 1 || f[0].Destination != listenerA {
			t.Errorf("flows = %+v", f)
		}
	})
}

// TestDefaultListenersNotShared checks that a configuration given the
// default listeners cannot change them for the next one.
func TestDefaultListenersNotShared(t *testing.T) {
	cfg := reflector.Config{}.WithDefaults()
	cfg.Listeners[0] = netip.MustParseAddrPort("192.0.2.1:9")
	if got := (reflector.Config{}).WithDefaults().Listeners[0]; got != reflector.DefaultListeners[0] || got == cfg.Listeners[0] {
		t.Errorf("the defaults changed to %v", got)
	}
}
