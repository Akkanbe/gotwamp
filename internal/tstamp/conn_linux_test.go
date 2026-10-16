//go:build linux

package tstamp

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type family struct {
	network  string
	loopback netip.Addr
}

var families = []family{
	{"udp4", netip.MustParseAddr("127.0.0.1")},
	{"udp6", netip.IPv6Loopback()},
}

// listenLoopback opens a Conn on the loopback address of f, skipping the test
// when the family is unavailable (for example, IPv6 disabled).
func listenLoopback(t *testing.T, f family, opts Options) Conn {
	t.Helper()
	c, err := Listen(f.network, netip.AddrPortFrom(f.loopback, 0), opts)
	if err != nil {
		if f.network == "udp6" {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		t.Fatalf("Listen(%s): %v", f.network, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readOne(t *testing.T, c Conn, b []byte) (int, netip.AddrPort, RxMeta) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, from, meta, err := c.ReadFrom(b)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	return n, from, meta
}

// awaitKernelTimestamp waits for Linux's deferred timestamp activation before
// the measured packets. Every probe is consumed, and missing support still fails.
func awaitKernelTimestamp(t *testing.T, tx, rx Conn) {
	t.Helper()
	readyBy := time.Now().Add(time.Second)
	if err := rx.SetReadDeadline(readyBy); err != nil {
		t.Fatalf("timestamp warm-up deadline: %v", err)
	}
	payload, buf := []byte("timestamp-warmup"), make([]byte, 64)
	for time.Now().Before(readyBy) {
		if _, err := tx.WriteTo(payload, rx.LocalAddr(), -1); err != nil {
			t.Fatalf("timestamp warm-up send: %v", err)
		}
		_, _, meta, err := rx.ReadFrom(buf)
		if err != nil {
			t.Fatalf("timestamp warm-up receive: %v", err)
		}
		if meta.Kernel {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("kernel receive timestamp did not become ready within 1s")
}

func TestLoopbackRoundTrip(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			// Loopback does not decrement the TTL, so the receiver sees what
			// the sender set.
			tx := listenLoopback(t, f, Options{TTL: 200, TOS: 0x28})
			rx := listenLoopback(t, f, Options{})
			// ModeAuto enables kernel timestamping when socket setup succeeds.
			if tx.Mode() != ModeKernel || rx.Mode() != ModeKernel {
				t.Fatalf("Mode() = %v, %v, want kernel", tx.Mode(), rx.Mode())
			}

			awaitKernelTimestamp(t, tx, rx)

			payload := []byte("twamp-test-packet")
			buf := make([]byte, 64)
			cases := []struct {
				name    string
				tos     int
				wantTOS int
			}{
				{"socket default TOS", -1, 0x28},
				{"per-packet TOS", 0xb8, 0xb8},
				{"per-packet zero TOS", 0, 0},
			}
			for _, tc := range cases {
				before := time.Now()
				sentAt, err := tx.WriteTo(payload, rx.LocalAddr(), tc.tos)
				if err != nil {
					t.Fatalf("%s: WriteTo: %v", tc.name, err)
				}
				n, from, meta := readOne(t, rx, buf)
				if !bytes.Equal(buf[:n], payload) {
					t.Errorf("%s: payload = %q, want %q", tc.name, buf[:n], payload)
				}
				if from != tx.LocalAddr() {
					t.Errorf("%s: from = %v, want %v", tc.name, from, tx.LocalAddr())
				}
				if meta.TTL != 200 {
					t.Errorf("%s: TTL = %d, want 200", tc.name, meta.TTL)
				}
				if meta.TOS != tc.wantTOS {
					t.Errorf("%s: TOS = %#x, want %#x", tc.name, meta.TOS, tc.wantTOS)
				}
				if !meta.Kernel {
					t.Errorf("%s: no kernel receive timestamp", tc.name)
				}
				if sentAt.Before(before) || meta.At.Before(sentAt) {
					t.Errorf("%s: timestamps out of order: before %v, sent %v, received %v", tc.name, before, sentAt, meta.At)
				}
				if sentAt.Round(0) != sentAt || meta.At.Round(0) != meta.At {
					t.Errorf("%s: timestamps carry a monotonic clock reading", tc.name)
				}
			}
		})
	}
}

func TestDefaultTTL(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			tx := listenLoopback(t, f, Options{})
			rx := listenLoopback(t, f, Options{})
			if _, err := tx.WriteTo([]byte{1}, rx.LocalAddr(), -1); err != nil {
				t.Fatalf("WriteTo: %v", err)
			}
			_, _, meta := readOne(t, rx, make([]byte, 8))
			if meta.TTL != 255 {
				t.Errorf("TTL = %d, want 255", meta.TTL)
			}
		})
	}
}

func TestReadDeadline(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			c := listenLoopback(t, f, Options{})
			if err := c.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
				t.Fatalf("SetReadDeadline: %v", err)
			}
			_, _, _, err := c.ReadFrom(make([]byte, 8))
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("ReadFrom error = %v, want os.ErrDeadlineExceeded", err)
			}
		})
	}
}

func TestReadAfterClose(t *testing.T) {
	c := listenLoopback(t, families[0], Options{})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.ReadFrom(make([]byte, 8))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("ReadFrom error = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadFrom did not return after Close")
	}
}

func TestTruncatedRead(t *testing.T) {
	f := families[0]
	tx := listenLoopback(t, f, Options{})
	rx := listenLoopback(t, f, Options{})
	if _, err := tx.WriteTo(make([]byte, 100), rx.LocalAddr(), -1); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if n, _, _ := readOne(t, rx, make([]byte, 10)); n != 10 {
		t.Errorf("n = %d, want 10 (truncated)", n)
	}
}

func TestIPv4MappedDestination(t *testing.T) {
	f := families[0]
	tx := listenLoopback(t, f, Options{})
	rx := listenLoopback(t, f, Options{})
	to := rx.LocalAddr()
	mapped := netip.AddrPortFrom(netip.AddrFrom16(to.Addr().As16()), to.Port())
	if _, err := tx.WriteTo([]byte{1}, mapped, -1); err != nil {
		t.Fatalf("WriteTo(%v): %v", mapped, err)
	}
	_, from, _ := readOne(t, rx, make([]byte, 8))
	if !from.Addr().Is4() {
		t.Errorf("from = %v, want a plain IPv4 address", from)
	}
}

func TestModes(t *testing.T) {
	f := families[0]
	for m, want := range map[Mode]Mode{ModeAuto: ModeKernel, ModeKernel: ModeKernel, ModeUserspace: ModeUserspace} {
		c := listenLoopback(t, f, Options{Mode: m})
		if c.Mode() != want {
			t.Errorf("Listen(Mode %v).Mode() = %v, want %v", m, c.Mode(), want)
		}
	}
}

// median returns the median of d, sorting it.
func median(d []time.Duration) time.Duration {
	slices.Sort(d)
	return d[len(d)/2]
}

// TestKernelRxTimestamps compares the kernel receive timestamp with the
// user-space time right after recvmsg: the kernel's comes first, and the
// difference is the user-space error the kernel mode removes.
func TestKernelRxTimestamps(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			tx := listenLoopback(t, f, Options{Mode: ModeUserspace})
			rx := listenLoopback(t, f, Options{Mode: ModeKernel})
			awaitKernelTimestamp(t, tx, rx)
			buf := make([]byte, 64)
			const n = 500
			var lag []time.Duration
			for range n {
				sentAt, err := tx.WriteTo(make([]byte, 41), rx.LocalAddr(), -1)
				if err != nil {
					t.Fatal(err)
				}
				_, _, meta := readOne(t, rx, buf)
				user := time.Now()
				if !meta.Kernel {
					t.Fatal("no kernel receive timestamp")
				}
				if meta.At.Before(sentAt) || meta.At.After(user) {
					t.Fatalf("kernel time %v outside [%v, %v]", meta.At, sentAt, user)
				}
				lag = append(lag, user.Sub(meta.At))
			}
			m := median(lag)
			t.Logf("%s: user-space receive time minus kernel timestamp: median %v, max %v over %d packets",
				f.network, m, lag[len(lag)-1], n)
			if m > 50*time.Millisecond {
				t.Errorf("median lag %v", m)
			}
		})
	}
}

func TestKernelTxTimestamps(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			c := listenLoopback(t, f, Options{Mode: ModeKernel, TxTimestamps: true})
			peer := listenLoopback(t, f, Options{})
			tt, ok := c.(TxTimestamper)
			if !ok || !tt.TxTimestamps() {
				t.Fatal("transmit timestamps unavailable")
			}
			const n = 200
			sent := make(map[uint32]time.Time, n)
			for i := range n {
				at, id, err := tt.WriteToID(make([]byte, 41), peer.LocalAddr(), -1)
				if err != nil {
					t.Fatal(err)
				}
				if id != uint32(i) {
					t.Fatalf("packet %d got id %d", i, id)
				}
				sent[id] = at
			}

			// Queued transmit timestamps do not disturb ordinary reads.
			if _, err := peer.WriteTo([]byte("reply"), c.LocalAddr(), -1); err != nil {
				t.Fatal(err)
			}
			if k, _, meta := readOne(t, c, make([]byte, 16)); k != 5 || !meta.Kernel {
				t.Errorf("read %d octets, kernel %v", k, meta.Kernel)
			}

			var delay []time.Duration
			seen := map[uint32]bool{}
			deadline := time.Now().Add(2 * time.Second)
			for len(seen) < n && time.Now().Before(deadline) {
				if _, err := tt.ReadTxTimestamps(func(id uint32, at time.Time) {
					userAt, ok := sent[id]
					if !ok || seen[id] {
						t.Errorf("unexpected id %d", id)
						return
					}
					seen[id] = true
					if at.Before(userAt) {
						t.Errorf("id %d: kernel transmit time %v before user-space %v", id, at, userAt)
					}
					delay = append(delay, at.Sub(userAt))
				}); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Millisecond)
			}
			if len(seen) != n {
				t.Fatalf("got %d of %d transmit timestamps", len(seen), n)
			}
			m := median(delay)
			t.Logf("%s: kernel transmit timestamp minus user-space send time: median %v, max %v over %d packets",
				f.network, m, delay[len(delay)-1], n)
			if m > 50*time.Millisecond {
				t.Errorf("median delay %v", m)
			}
		})
	}
}

func TestTxTimestampsUnavailable(t *testing.T) {
	f := families[0]
	for _, opts := range []Options{
		{Mode: ModeUserspace, TxTimestamps: true},
		{Mode: ModeKernel},
	} {
		tt := listenLoopback(t, f, opts).(TxTimestamper)
		if tt.TxTimestamps() {
			t.Errorf("%+v: TxTimestamps() = true", opts)
		}
		if _, _, err := tt.WriteToID([]byte{1}, netip.MustParseAddrPort("127.0.0.1:9"), -1); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v: WriteToID error %v", opts, err)
		}
		if _, err := tt.ReadTxTimestamps(func(uint32, time.Time) {}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v: ReadTxTimestamps error %v", opts, err)
		}
	}
}

func TestKernelHotPathDoesNotAllocate(t *testing.T) {
	f := families[0]
	tx := listenLoopback(t, f, Options{Mode: ModeKernel, TxTimestamps: true})
	rx := listenLoopback(t, f, Options{Mode: ModeKernel})
	tt := tx.(TxTimestamper)
	payload, buf := make([]byte, 41), make([]byte, 128)
	to := rx.LocalAddr()
	if err := rx.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var got int
	fn := func(uint32, time.Time) { got++ }
	allocs := testing.AllocsPerRun(100, func() {
		if _, _, err := tt.WriteToID(payload, to, 0xb8); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := rx.ReadFrom(buf); err != nil {
			t.Fatal(err)
		}
		if _, err := tt.ReadTxTimestamps(fn); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("WriteToID+ReadFrom+ReadTxTimestamps allocate %v times per packet, want 0", allocs)
	}
	if got == 0 {
		t.Error("no transmit timestamps read")
	}
}

func TestListenErrors(t *testing.T) {
	lo := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 0)
	cases := []struct {
		name    string
		network string
		local   netip.AddrPort
		opts    Options
	}{
		{"network", "tcp", lo, Options{}},
		{"TTL high", "udp4", lo, Options{TTL: 256}},
		{"TTL negative", "udp4", lo, Options{TTL: -1}},
		{"TOS", "udp4", lo, Options{TOS: 256}},
		{"read buffer", "udp4", lo, Options{ReadBuffer: -1}},
		{"mode", "udp4", lo, Options{Mode: ModeHardware + 1}},
		{"family mismatch", "udp4", netip.AddrPortFrom(netip.IPv6Loopback(), 0), Options{}},
	}
	for _, tc := range cases {
		if c, err := Listen(tc.network, tc.local, tc.opts); err == nil {
			_ = c.Close()
			t.Errorf("%s: Listen succeeded, want an error", tc.name)
		}
	}
}

func TestWriteToErrors(t *testing.T) {
	f := families[0]
	c := listenLoopback(t, f, Options{})
	if _, err := c.WriteTo([]byte{1}, c.LocalAddr(), 256); err == nil {
		t.Error("WriteTo(tos 256) succeeded, want an error")
	}
	// An IPv6 destination cannot be reached from an IPv4 socket.
	if _, err := c.WriteTo([]byte{1}, netip.AddrPortFrom(netip.IPv6Loopback(), 9), -1); err == nil {
		t.Error("WriteTo(IPv6 destination) on udp4 succeeded, want an error")
	}
}

func TestWildcardListen(t *testing.T) {
	c, err := Listen("udp4", netip.AddrPort{}, Options{})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer c.Close()
	if c.LocalAddr().Port() == 0 {
		t.Errorf("LocalAddr() = %v, want a kernel-assigned port", c.LocalAddr())
	}
}

func TestHotPathDoesNotAllocate(t *testing.T) {
	for _, f := range families {
		t.Run(f.network, func(t *testing.T) {
			tx := listenLoopback(t, f, Options{})
			rx := listenLoopback(t, f, Options{})
			payload, buf := make([]byte, 41), make([]byte, 128)
			to := rx.LocalAddr()
			if err := rx.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			allocs := testing.AllocsPerRun(100, func() {
				if _, err := tx.WriteTo(payload, to, 0xb8); err != nil {
					t.Fatal(err)
				}
				if _, _, _, err := rx.ReadFrom(buf); err != nil {
					t.Fatal(err)
				}
			})
			if allocs != 0 {
				t.Errorf("WriteTo+ReadFrom allocate %v times per packet, want 0", allocs)
			}
		})
	}
}

// TestSourceAddress: a socket bound to the wildcard address learns the
// address it was reached at and answers from it (SPEC §6.1).
func TestSourceAddress(t *testing.T) {
	srv, err := Listen("udp4", netip.MustParseAddrPort("0.0.0.0:0"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cli := listenLoopback(t, families[0], Options{})
	// All of 127/8 is local; 127.0.0.2 is not the route's choice of source.
	alias := netip.MustParseAddr("127.0.0.2")
	if _, err := cli.WriteTo([]byte{1}, netip.AddrPortFrom(alias, srv.LocalAddr().Port()), -1); err != nil {
		t.Fatal(err)
	}
	_, from, meta := readOne(t, srv, make([]byte, 8))
	if meta.Dst != alias || meta.IfIndex == 0 {
		t.Fatalf("Dst %v, IfIndex %d", meta.Dst, meta.IfIndex)
	}
	sw := srv.(SourceWriter)
	for _, tos := range []int{-1, 0xb8} {
		if _, err := sw.WriteToFrom([]byte{2}, from, meta.Dst, tos); err != nil {
			t.Fatal(err)
		}
		_, replyFrom, rmeta := readOne(t, cli, make([]byte, 8))
		if replyFrom.Addr() != alias || tos >= 0 && rmeta.TOS != tos {
			t.Errorf("tos %d: reply from %v with TOS %#x", tos, replyFrom, rmeta.TOS)
		}
	}
	// Without a source the route picks 127.0.0.1.
	if _, err := srv.WriteTo([]byte{3}, from, -1); err != nil {
		t.Fatal(err)
	}
	if _, replyFrom, _ := readOne(t, cli, make([]byte, 8)); replyFrom.Addr() == alias {
		t.Errorf("a reply without a source came from %v", replyFrom)
	}
	if _, err := sw.WriteToFrom([]byte{4}, from, netip.IPv6Loopback(), -1); err == nil {
		t.Error("an IPv6 source on an IPv4 socket was accepted")
	}

	// IPv6: the destination is reported too.
	srv6, err := Listen("udp6", netip.MustParseAddrPort("[::]:0"), Options{})
	if err != nil {
		t.Skipf("IPv6 unavailable: %v", err)
	}
	defer srv6.Close()
	cli6 := listenLoopback(t, families[1], Options{})
	if _, err := cli6.WriteTo([]byte{1}, netip.AddrPortFrom(netip.IPv6Loopback(), srv6.LocalAddr().Port()), -1); err != nil {
		t.Fatal(err)
	}
	_, from6, meta6 := readOne(t, srv6, make([]byte, 8))
	if meta6.Dst != netip.IPv6Loopback() {
		t.Fatalf("Dst %v", meta6.Dst)
	}
	if _, err := srv6.(SourceWriter).WriteToFrom([]byte{2}, from6, meta6.Dst, 0x28); err != nil {
		t.Fatal(err)
	}
	if _, replyFrom, _ := readOne(t, cli6, make([]byte, 8)); replyFrom.Addr() != netip.IPv6Loopback() {
		t.Errorf("reply from %v", replyFrom)
	}
}

// TestDFCleared: path MTU discovery is off, so IPv4 packets go without DF
// (SPEC §6.1).
func TestDFCleared(t *testing.T) {
	for _, f := range families {
		c := listenLoopback(t, f, Options{}).(*udpConn)
		rc, err := c.c.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		level, opt, want := unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DONT
		if f.network == "udp6" {
			level, opt, want = unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DONT
		}
		var got int
		var gerr error
		_ = rc.Control(func(fd uintptr) { got, gerr = unix.GetsockoptInt(int(fd), level, opt) })
		if gerr != nil || got != want {
			t.Errorf("%s: MTU discover = %d, %v; want %d", f.network, got, gerr, want)
		}
	}
}

func TestWriteToFromDoesNotAllocate(t *testing.T) {
	tx := listenLoopback(t, families[0], Options{}).(SourceWriter)
	rx := listenLoopback(t, families[0], Options{})
	payload, buf := make([]byte, 41), make([]byte, 128)
	if err := rx.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	src := netip.MustParseAddr("127.0.0.1")
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := tx.WriteToFrom(payload, rx.LocalAddr(), src, 0xb8); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := rx.ReadFrom(buf); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("WriteToFrom+ReadFrom allocate %v times per packet", allocs)
	}
}
