package reflector_test

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
)

func TestConfigurationBeforeIO(t *testing.T) {
	light := (reflector.Config{}).WithDefaults()
	if err := light.Validate(); err != nil {
		t.Fatal(err)
	}
	full := (reflector.FullConfig{}).WithDefaults()
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	full.Modes = gotwamp.ModeEncrypted
	if err := full.Validate(); err == nil {
		t.Fatal("encrypted mode accepted without credentials")
	}
	full.Credentials = gotwamp.Credentials{"test": []byte("test secret")}
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	light.Timestamping = 255
	if _, err := reflector.ListenLight(light, reflector.Deps{}); err == nil {
		t.Fatal("invalid timestamping accepted")
	}
	full.Timestamping = 255
	if _, err := reflector.ListenFull(full, reflector.Deps{}); err == nil {
		t.Fatal("invalid timestamping accepted")
	}
	light.Timestamping, light.ReadBuffer = gotwamp.TimestampAuto, -1
	if _, err := reflector.ListenLight(light, reflector.Deps{}); err == nil {
		t.Fatal("invalid receive buffer accepted")
	}
}

func TestFullCloseBeforeServe(t *testing.T) {
	f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Serve(t.Context()); err == nil {
		t.Fatal("Serve after Close succeeded")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFullServeCloseRace(t *testing.T) {
	for range 50 {
		f, err := reflector.ListenFull(reflector.FullConfig{Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")}}, reflector.Deps{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- f.Serve(ctx) }()
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		cancel()
		wg.Wait()
		<-done // Either already serving or closed before serving are valid.
		if st := f.Status(); st.Control.ConnectionsActive != 0 || st.Control.SessionsActive != 0 {
			t.Errorf("resources remain: %+v", st)
		}
	}
}
