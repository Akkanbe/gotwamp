package sender

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestFullPreflightBeforeIO(t *testing.T) {
	server := netip.MustParseAddrPort("127.0.0.1:862")
	for name, edit := range map[string]func(*Config, *FullConfig){
		"missing secret": func(_ *Config, f *FullConfig) { f.Modes = wire.ModeEncrypted; f.KeyID = "test" },
		"invalid key id": func(_ *Config, f *FullConfig) {
			f.Modes = wire.ModeAuthenticated
			f.KeyID = string(make([]byte, 81))
			f.Passphrase = []byte("test")
		},
		"unknown security":                 func(_ *Config, f *FullConfig) { f.Modes = 1 << 20 },
		"unsupported feature":              func(_ *Config, f *FullConfig) { f.Features = wire.ModePTPv2Timestamp },
		"timestamping":                     func(c *Config, _ *FullConfig) { c.Timestamping = 255 },
		"read buffer":                      func(c *Config, _ *FullConfig) { c.ReadBuffer = -1 },
		"method error":                     func(c *Config, _ *FullConfig) { c.MethodError = -1 },
		"reflect length without feature":   func(_ *Config, f *FullConfig) { f.PaddingToReflect = 1 },
		"reflected octets without feature": func(_ *Config, f *FullConfig) { f.OctetsToReflect = 1 },
		"impossible reflect padding": func(c *Config, f *FullConfig) {
			c.Padding = 8
			f.Features = wire.ModeReflectOctets | wire.ModeSymmetricalSize
			f.PaddingToReflect = 8
		},
		"full dscp ecn monitoring": func(c *Config, _ *FullConfig) { c.DSCPECNMonitoring = true },
		"samples":                  func(c *Config, _ *FullConfig) { c.MaxSamples = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg, full := DefaultConfig(server), FullConfig{Server: server}
			edit(&cfg, &full)
			dialed, listened := false, false
			full = WithFullDial(full, func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
				dialed = true
				return nil, errors.New("unexpected dial")
			})
			full = WithFullListen(full, func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) {
				listened = true
				return nil, errors.New("unexpected listen")
			})
			if _, err := RunFull(t.Context(), cfg, full); err == nil || dialed || listened {
				t.Fatalf("error=%v dialed=%v listened=%v", err, dialed, listened)
			}
		})
	}
}

func TestLightPreflight(t *testing.T) {
	for _, edit := range []func(*Config){
		func(c *Config) { c.Timestamping = 255 },
		func(c *Config) { c.ReadBuffer = -1 },
	} {
		cfg := DefaultConfig(netip.MustParseAddrPort("127.0.0.1:862"))
		edit(&cfg)
		if _, err := New(cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func TestFullPreflightAllowsNegotiableLayout(t *testing.T) {
	server := netip.MustParseAddrPort("127.0.0.1:862")
	for _, tc := range []struct {
		name            string
		modes, features wire.Modes
		padding         int
	}{
		{"security preference", wire.ModeAuthenticated | wire.ModeUnauthenticated, wire.ModeReflectOctets, 30},
		{"optional symmetrical size", wire.ModeAuthenticated, wire.ModeReflectOctets | wire.ModeSymmetricalSize, 8},
		{"mixed test packets", wire.ModeMixed, wire.ModeReflectOctets, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig(server)
			cfg.Padding = tc.padding
			full := FullConfig{Server: server, Modes: tc.modes, Features: tc.features, PaddingToReflect: 1, KeyID: "test", Passphrase: []byte("test secret")}
			if err := ValidateFull(cfg, full); err != nil {
				t.Fatal(err)
			}
		})
	}
}
