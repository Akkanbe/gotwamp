//go:build linux

package reflector_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestLoopback reflects real UDP packets through tstamp sockets on the IPv4
// and IPv6 loopback addresses.
func TestLoopback(t *testing.T) {
	for _, tc := range []struct {
		name, network string
		listen        netip.AddrPort
	}{
		{"ipv4", "udp4", netip.MustParseAddrPort("127.0.0.1:0")},
		{"ipv6", "udp6", netip.MustParseAddrPort("[::1]:0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			light, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{tc.listen}}, reflector.Deps{})
			if err != nil {
				t.Skipf("cannot listen on %v: %v", tc.listen, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error)
			go func() { done <- light.Serve(ctx) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("Serve: %v", err)
				}
			}()

			addr := light.Addrs()[0]
			client, err := net.ListenUDP(tc.network, &net.UDPAddr{IP: addr.Addr().AsSlice()})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			buf := make([]byte, 2048)
			for seq := range uint32(3) {
				t1 := time.Now()
				p := wire.SenderPacket{Seq: 100 + seq, Timestamp: ntptime.FromTime(t1), ErrorEstimate: errest.Compute(time.Millisecond, false), Padding: make([]byte, wire.DefaultPaddingLength)}
				if _, err := client.WriteToUDPAddrPort(p.Append(nil), addr); err != nil {
					t.Fatal(err)
				}
				if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				n, from, err := client.ReadFromUDPAddrPort(buf)
				if err != nil {
					t.Fatal(err)
				}
				t4 := time.Now()
				if from.Port() != addr.Port() {
					t.Errorf("reply from %v, want port %d", from, addr.Port())
				}
				var r wire.ReflectorPacket
				if err := r.Decode(buf[:n]); err != nil {
					t.Fatal(err)
				}
				if n != 41 || r.Seq != seq || r.SenderSeq != 100+seq || r.SenderTimestamp != p.Timestamp {
					t.Errorf("reply %d octets: %+v", n, r)
				}
				if r.SenderTTL != 64 {
					t.Errorf("Sender TTL = %d, want 64 (the client's default)", r.SenderTTL)
				}
				t2, t3 := r.ReceiveTimestamp.Time(t1), r.Timestamp.Time(t1)
				if t2.Before(t1.Add(-time.Millisecond)) || t3.Before(t2) || t3.After(t4.Add(time.Millisecond)) {
					t.Errorf("timestamps out of order: T1 %v T2 %v T3 %v T4 %v", t1, t2, t3, t4)
				}
			}
			// The reflector counts a reply after sendmsg returns, which may
			// be just after the client has already read it.
			eventually(t, func() bool { return light.Status().Counters.Reflected == 3 })
			st := light.Status()
			if st.Counters.Reflected != 3 || st.FlowsActive != 1 {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

// TestLoopbackWildcardSource sends to 127.0.0.2 through a Light listener on
// 0.0.0.0: the reply must come from 127.0.0.2, not from the address the route
// to the client would pick (127.0.0.1), or senders that check the source drop
// it (SPEC §6.1, audit W1).
func TestLoopbackWildcardSource(t *testing.T) {
	light, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:0")}}, reflector.Deps{})
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- light.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	port := light.Addrs()[0].Port()

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	buf := make([]byte, 2048)
	for _, dst := range []string{"127.0.0.2", "127.0.0.1", "127.0.0.3"} {
		to := netip.AddrPortFrom(netip.MustParseAddr(dst), port)
		p := wire.SenderPacket{Padding: make([]byte, wire.DefaultPaddingLength)}
		if _, err := client.WriteToUDPAddrPort(p.Append(nil), to); err != nil {
			t.Fatal(err)
		}
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, from, err := client.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatal(err)
		}
		if from != to {
			t.Errorf("reply to a packet for %v came from %v", to, from)
		}
	}
	flows := light.Flows()
	if len(flows) != 3 {
		t.Errorf("flows = %+v, want one per local address", flows)
	}
}
