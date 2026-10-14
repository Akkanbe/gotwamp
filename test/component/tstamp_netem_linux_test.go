//go:build integration && linux

package component

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// TestTstampAcrossNetemDelay sends packets from A to B over a veth delayed by
// netem and checks that the timestamps see the delay, and that TTL and TOS
// cross the link as set.
func TestTstampAcrossNetemDelay(t *testing.T) {
	tp := newTopology(t)
	const delay = 10 * time.Millisecond
	tp.netem(tp.a, "delay", "10ms")

	for _, c := range []struct {
		network string
		a, b    netip.Addr
	}{
		{"udp4", tp.addrA4, tp.addrB4},
		{"udp6", tp.addrA6, tp.addrB6},
	} {
		t.Run(c.network, func(t *testing.T) {
			var tx, rx tstamp.Conn
			var err error
			tp.in(tp.a, func() { tx, err = tstamp.Listen(c.network, netip.AddrPortFrom(c.a, 0), tstamp.Options{}) })
			if err != nil {
				t.Fatalf("Listen in A: %v", err)
			}
			defer tx.Close()
			tp.in(tp.b, func() { rx, err = tstamp.Listen(c.network, netip.AddrPortFrom(c.b, 0), tstamp.Options{}) })
			if err != nil {
				t.Fatalf("Listen in B: %v", err)
			}
			defer rx.Close()

			buf := make([]byte, 128)
			for i := range 20 {
				sentAt, err := tx.WriteTo(make([]byte, 41), rx.LocalAddr(), 0xb8)
				if err != nil {
					t.Fatalf("WriteTo: %v", err)
				}
				if err := rx.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				_, from, meta, err := rx.ReadFrom(buf)
				if err != nil {
					t.Fatalf("packet %d: ReadFrom: %v", i, err)
				}
				if from.Addr() != c.a {
					t.Errorf("packet %d: from %v, want %v", i, from, c.a)
				}
				if owd := meta.At.Sub(sentAt); owd < delay || owd > delay+5*time.Millisecond {
					t.Errorf("packet %d: one-way delay %v, want %v..%v", i, owd, delay, delay+5*time.Millisecond)
				}
				if meta.TTL != 255 || meta.TOS != 0xb8 {
					t.Errorf("packet %d: TTL %d TOS %#x, want 255 and 0xb8", i, meta.TTL, meta.TOS)
				}
			}
		})
	}
}
