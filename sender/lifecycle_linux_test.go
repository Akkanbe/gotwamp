//go:build linux

package sender_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
	"github.com/Akkanbe/gotwamp/sender"
)

func TestSenderCloseIsIdempotent(t *testing.T) {
	cfg := sender.DefaultConfig(netip.MustParseAddrPort("127.0.0.1:862"))
	cfg.Timestamping = gotwamp.TimestampUserspace
	s, err := sender.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !s.LocalAddr().IsValid() {
		t.Error("missing bound endpoint")
	}
	for range 2 {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContinuousLightCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	l, err := reflector.ListenLight(reflector.Config{
		Listeners:    []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		Timestamping: gotwamp.TimestampUserspace,
	}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Serve(ctx) }()
	defer func() {
		if err := l.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cfg := sender.DefaultConfig(l.Addrs()[0])
	cfg.Count, cfg.Interval = 0, time.Millisecond
	cfg.Timestamping = gotwamp.TimestampUserspace
	summary, err := sender.RunLight(runCtx, cfg, sender.WithHooks(sender.Hooks{
		Packet: func(p sender.PacketResult) {
			if p.Result.Kind == gotwamp.KindAccepted {
				stop()
			}
		},
	}))
	if !errors.Is(err, context.Canceled) || !summary.Aborted || summary.Received == 0 || summary.Invalid || summary.OWDValid {
		t.Fatalf("summary=%+v error=%v", summary, err)
	}
}
