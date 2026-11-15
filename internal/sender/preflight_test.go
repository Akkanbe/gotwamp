//declscope:namespace sender // tests the configuration checks of sender.go and full.go

package sender

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// errDialed is what the dialer of preflightConfig returns: an error that
// matches it means RunFull got past its checks.
var errDialed = errors.New("dialed")

// preflightConfig returns a Full configuration whose dialer and listener
// record that they were called and fail.
func preflightConfig(dialed, listened *bool) FullConfig {
	return FullConfig{
		Server: netip.MustParseAddrPort("127.0.0.1:862"),
		Dial: func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
			*dialed = true
			return nil, errDialed
		},
		Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) {
			*listened = true
			return nil, errors.New("listened")
		},
	}
}

func TestFullPreflightBeforeIO(t *testing.T) {
	for name, tc := range map[string]struct {
		edit  func(*Config, *FullConfig)
		stage client.Stage // of the control error; 0 for another error
	}{
		"missing passphrase": {func(_ *Config, f *FullConfig) { f.Modes = wire.ModeEncrypted; f.KeyID = "test" }, client.StageSetup},
		"key id too long": {func(_ *Config, f *FullConfig) {
			f.Modes, f.KeyID, f.Passphrase = wire.ModeAuthenticated, string(make([]byte, 81)), []byte("test")
		}, client.StageSetup},
		"not a security mode":                    {func(_ *Config, f *FullConfig) { f.Modes = 1 << 20 }, client.StageSetup},
		"unsupported feature":                    {func(_ *Config, f *FullConfig) { f.Features = wire.ModePTPv2Timestamp }, client.StageSetup},
		"unknown timestamping":                   {func(c *Config, _ *FullConfig) { c.Timestamping = 255 }, 0},
		"negative read buffer":                   {func(c *Config, _ *FullConfig) { c.ReadBuffer = -1 }, 0},
		"negative method error":                  {func(c *Config, _ *FullConfig) { c.MethodError = -1 }, 0},
		"negative userspace error":               {func(c *Config, _ *FullConfig) { c.UserspaceError = -1 }, 0},
		"dscp ecn monitoring":                    {func(c *Config, _ *FullConfig) { c.DSCPECNMonitoring = true }, 0},
		"padding to reflect without the feature": {func(_ *Config, f *FullConfig) { f.PaddingToReflect = 1 }, client.StageRequest},
		"octets to reflect without the feature":  {func(_ *Config, f *FullConfig) { f.OctetsToReflect = 1 }, client.StageRequest},
		"padding too short in every mode": {func(c *Config, f *FullConfig) {
			c.Padding, f.Features, f.PaddingToReflect = 8, wire.ModeReflectOctets|wire.ModeSymmetricalSize, 8
		}, client.StageRequest},
		"padding too short for the authenticated mode": {func(c *Config, f *FullConfig) {
			c.Padding, f.Features, f.PaddingToReflect = 30, wire.ModeReflectOctets, 1
			f.Modes, f.KeyID, f.Passphrase = wire.ModeAuthenticated, "test", []byte("test secret")
		}, client.StageRequest},
	} {
		t.Run(name, func(t *testing.T) {
			var dialed, listened bool
			cfg, fc := DefaultConfig(netip.AddrPort{}), preflightConfig(&dialed, &listened)
			tc.edit(&cfg, &fc)
			_, err := RunFull(t.Context(), cfg, fc)
			if err == nil || dialed || listened {
				t.Fatalf("error=%v dialed=%v listened=%v", err, dialed, listened)
			}
			var stage client.Stage
			if ce, ok := errors.AsType[*client.Error](err); ok {
				stage = ce.Stage
			}
			if stage != tc.stage {
				t.Errorf("error %v: stage %v, want %v", err, stage, tc.stage)
			}
		})
	}
}

// TestFullPreflightAccepts checks that the checks before dialing pass every
// configuration that some negotiation can accept.
func TestFullPreflightAccepts(t *testing.T) {
	for _, tc := range []struct {
		name            string
		modes, features wire.Modes
		padding         int
		toReflect       uint16
	}{
		{"every feature", wire.ModeEncrypted | wire.ModeUnauthenticated, client.FeatureModes, 27, 0},
		{"one mode of several fits", wire.ModeAuthenticated | wire.ModeUnauthenticated, wire.ModeReflectOctets, 30, 1},
		{"symmetrical size", wire.ModeAuthenticated, wire.ModeReflectOctets | wire.ModeSymmetricalSize, 8, 1},
		{"mixed mode sends unauthenticated test packets", wire.ModeMixed, wire.ModeReflectOctets, 30, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed, listened bool
			cfg, fc := DefaultConfig(netip.AddrPort{}), preflightConfig(&dialed, &listened)
			cfg.Padding = tc.padding
			fc.Modes, fc.Features, fc.PaddingToReflect = tc.modes, tc.features, tc.toReflect
			fc.KeyID, fc.Passphrase = "test", []byte("test secret")
			if _, err := RunFull(t.Context(), cfg, fc); !errors.Is(err, errDialed) || !dialed {
				t.Fatalf("error=%v dialed=%v, want the dial error", err, dialed)
			}
		})
	}
}

func TestLightPreflight(t *testing.T) {
	for name, edit := range map[string]func(*Config){
		"unknown timestamping":  func(c *Config) { c.Timestamping = 255 },
		"negative read buffer":  func(c *Config) { c.ReadBuffer = -1 },
		"negative kernel error": func(c *Config) { c.KernelError = -1 },
	} {
		cfg := DefaultConfig(netip.MustParseAddrPort("127.0.0.1:862"))
		edit(&cfg)
		if err := normalize(&cfg, false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
