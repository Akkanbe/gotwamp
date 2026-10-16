//go:build linux

package sender_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
	"github.com/Akkanbe/gotwamp/sender"
)

func TestMappedIPv4SenderEndpoints(t *testing.T) {
	mapped, ok := netip.AddrFromSlice(net.ParseIP("127.0.0.1"))
	if !ok || !mapped.Is4In6() {
		t.Fatal("expected mapped IPv4 from net.IP conversion")
	}
	for _, full := range []bool{false, true} {
		for _, mapping := range []string{"source", "target", "both"} {
			name := "light/" + mapping
			if full {
				name = "full/" + mapping
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var target netip.AddrPort
				var serve func(context.Context) error
				var closeService func() error
				if full {
					f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}, Timestamping: gotwamp.TimestampUserspace}, reflector.Deps{})
					if err != nil {
						t.Fatal(err)
					}
					target, serve, closeService = f.Addrs()[0], f.Serve, f.Close
				} else {
					l, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}, Timestamping: gotwamp.TimestampUserspace}, reflector.Deps{})
					if err != nil {
						t.Fatal(err)
					}
					target, serve, closeService = l.Addrs()[0], l.Serve, l.Close
				}
				served := make(chan error, 1)
				go func() { served <- serve(ctx) }()
				defer func() {
					if err := closeService(); err != nil {
						t.Error(err)
					}
					if err := <-served; err != nil {
						t.Error(err)
					}
				}()
				cfg := sender.DefaultConfig(target)
				cfg.Count, cfg.Interval, cfg.Timeout = 3, 5*time.Millisecond, 500*time.Millisecond
				cfg.Timestamping = gotwamp.TimestampUserspace
				cfg.Source = netip.MustParseAddrPort("127.0.0.1:0")
				if mapping == "source" || mapping == "both" {
					cfg.Source = netip.AddrPortFrom(mapped, 0)
				}
				if mapping == "target" || mapping == "both" {
					target = netip.AddrPortFrom(mapped, target.Port())
					cfg.Target = target
				}
				var sum sender.Summary
				var err error
				if full {
					fc := sender.FullConfig{Server: target}
					if err := sender.ValidateFull(cfg, fc); err != nil {
						t.Fatal(err)
					}
					sum, err = sender.RunFull(ctx, cfg, fc)
				} else {
					if err := cfg.Validate(); err != nil {
						t.Fatal(err)
					}
					s, openErr := sender.New(cfg)
					if openErr != nil {
						t.Fatal(openErr)
					}
					if !s.LocalAddr().Addr().Is4() {
						t.Error("bound source was not canonical IPv4")
					}
					sum, err = s.Run(ctx)
					if closeErr := s.Close(); closeErr != nil {
						t.Error(closeErr)
					}
				}
				if err != nil || sum.Received != 3 || sum.Invalid || sum.Aborted || !sum.Local.Addr().Is4() || !sum.Remote.Addr().Is4() {
					t.Fatalf("summary=%+v error=%v", sum, err)
				}
			})
		}
	}
}
