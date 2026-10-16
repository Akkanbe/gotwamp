//go:build linux

package reflector_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
	"github.com/Akkanbe/gotwamp/sender"
)

// This test imports only public packages and exercises both UDP and TCP paths.
func TestPublicFullSecurityMatrix(t *testing.T) {
	for _, mode := range []gotwamp.Modes{gotwamp.ModeUnauthenticated, gotwamp.ModeAuthenticated, gotwamp.ModeEncrypted, gotwamp.ModeMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			f, err := reflector.ListenFull(reflector.FullConfig{
				Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
				Modes:     mode, Credentials: gotwamp.Credentials{"test": []byte("test secret")}, Timestamping: gotwamp.TimestampUserspace,
			}, reflector.Deps{})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- f.Serve(ctx) }()
			defer func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			cfg := sender.DefaultConfig(netip.AddrPort{})
			cfg.Count, cfg.Interval, cfg.Timeout = 3, 5*time.Millisecond, 500*time.Millisecond
			cfg.Timestamping = gotwamp.TimestampUserspace
			summary, err := sender.RunFull(ctx, cfg, sender.FullConfig{Server: f.Addrs()[0], Modes: mode, KeyID: "test", Passphrase: []byte("test secret")})
			if err != nil {
				t.Fatal(err)
			}
			if summary.Sent != 3 || summary.Received != 3 || summary.Lost != 0 || summary.Aborted {
				t.Fatalf("summary: %+v", summary)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			st := f.Status()
			if st.Counters.Reflected != 3 || st.Control.ConnectionsAccepted != 1 || st.Control.SessionsActive != 0 {
				t.Errorf("status: %+v", st)
			}
		})
	}
}

func TestPublicLightLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	l, err := reflector.ListenLight(reflector.Config{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}, Timestamping: gotwamp.TimestampUserspace}, reflector.Deps{})
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
	cfg := sender.DefaultConfig(l.Addrs()[0])
	cfg.Count, cfg.Interval, cfg.Timeout = 3, 5*time.Millisecond, 500*time.Millisecond
	cfg.Timestamping = gotwamp.TimestampUserspace
	summary, err := sender.RunLight(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Sent != 3 || summary.Received != 3 || summary.Aborted {
		t.Fatalf("summary: %+v", summary)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if st := l.Status(); st.Counters.Reflected != 3 || st.FlowsActive != 1 {
		t.Errorf("status: %+v", st)
	}
	if len(l.Flows()) != 1 {
		t.Error("missing Light flow")
	}
}
