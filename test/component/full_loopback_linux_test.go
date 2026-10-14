//go:build linux

package component

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
)

// TestFullLoopback runs TWAMP Full end to end on the loopback interfaces,
// without root: gotwampd's control server with the Full reflector on one
// side, a Full run of the sender on the other, and checks the statistics.
func TestFullLoopback(t *testing.T) {
	for _, c := range []struct {
		name string
		addr netip.Addr
	}{
		{"ipv4", netip.MustParseAddr("127.0.0.1")},
		{"ipv6", netip.MustParseAddr("::1")},
	} {
		t.Run(c.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", netip.AddrPortFrom(c.addr, 0).String())
			if err != nil {
				t.Skipf("cannot listen on %v: %v", c.addr, err)
			}
			full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{})
			if err != nil {
				t.Fatal(err)
			}
			srv, err := server.New(server.Config{}, full, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			served := make(chan error)
			go func() { served <- srv.Serve(ctx, ln) }()
			defer func() {
				cancel()
				if err := <-served; err != nil {
					t.Errorf("Serve: %v", err)
				}
			}()

			cfg := sender.DefaultConfig(netip.AddrPort{})
			cfg.Count = 50
			cfg.Interval = 2 * time.Millisecond
			cfg.Timeout = time.Second
			cfg.DSCP = 46
			var packets []sender.PacketResult
			hooks := sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) { packets = append(packets, r) }})
			clock := sender.WithClock(func() (bool, time.Duration) { return true, time.Millisecond })
			sum, err := sender.RunFull(t.Context(), cfg, sender.FullConfig{Server: ln.Addr().(*net.TCPAddr).AddrPort()}, hooks, clock)
			if err != nil {
				t.Fatal(err)
			}

			if sum.Sent != 50 || sum.Received != 50 || sum.Lost != 0 || sum.DupForward+sum.DupReverse != 0 || sum.Reordered != 0 || sum.Aborted {
				t.Errorf("counts = %+v", sum.Summary)
			}
			if sum.RTT.Count != 50 || sum.RTT.Min <= 0 || sum.RTT.Max > 100*time.Millisecond || sum.RTT.Min > sum.RTT.Median {
				t.Errorf("rtt = %+v", sum.RTT)
			}
			if sum.ReflectorDelay.Min < 0 || sum.ReflectorDelay.Max > sum.RTT.Max+time.Second {
				t.Errorf("reflector delay = %+v", sum.ReflectorDelay)
			}
			// Loopback: no hop is crossed in either direction.
			if sum.ForwardHops.Min != 0 || sum.ForwardHops.Max != 0 || sum.ReverseHops.Max != 0 {
				t.Errorf("hops forward %+v reverse %+v", sum.ForwardHops, sum.ReverseHops)
			}
			if !sum.SenderSynced || sum.ReflectorSynced {
				t.Errorf("synced sender %v reflector %v; the reflector has no clock here", sum.SenderSynced, sum.ReflectorSynced)
			}
			if sum.Remote.Addr() != c.addr || sum.Local.Addr() != c.addr {
				t.Errorf("local %v remote %v", sum.Local, sum.Remote)
			}
			for i, p := range packets {
				r := p.Record
				if r.SenderSeq != uint32(i) || r.ReflectorSeq != uint32(i) {
					t.Errorf("packet %d: sender seq %d, reflector seq %d (a Full reflector counts per session)", i, r.SenderSeq, r.ReflectorSeq)
				}
				if p.ReflectorErrorEstimate != errest.Unknown || !p.SenderErrorEstimate.Synchronized() {
					t.Errorf("packet %d: error estimates sender %v reflector %v", i, p.SenderErrorEstimate, p.ReflectorErrorEstimate)
				}
				if p.TOS>>2 != 46 {
					t.Errorf("packet %d: reply DSCP %d, want the Type-P DSCP 46", i, p.TOS>>2)
				}
			}
			// The reflector counts a reply after sendmsg returns, which may be
			// just after the sender has already read it.
			deadline := time.Now().Add(5 * time.Second)
			for full.Counters().Reflected != 50 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := full.Counters(); got.Reflected != 50 {
				t.Errorf("reflector counters = %+v", got)
			}
		})
	}
}
