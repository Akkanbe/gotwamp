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
		setup bool // the error is a control error of the setup stage
	}{
		"missing passphrase": {func(_ *Config, f *FullConfig) { f.Modes = wire.ModeEncrypted; f.KeyID = "test" }, true},
		"key id too long": {func(_ *Config, f *FullConfig) {
			f.Modes, f.KeyID, f.Passphrase = wire.ModeAuthenticated, string(make([]byte, 81)), []byte("test")
		}, true},
		"not a security mode":      {func(_ *Config, f *FullConfig) { f.Modes = 1 << 20 }, true},
		"unsupported feature":      {func(_ *Config, f *FullConfig) { f.Features = wire.ModePTPv2Timestamp }, true},
		"unknown timestamping":     {func(c *Config, _ *FullConfig) { c.Timestamping = 255 }, false},
		"negative read buffer":     {func(c *Config, _ *FullConfig) { c.ReadBuffer = -1 }, false},
		"negative method error":    {func(c *Config, _ *FullConfig) { c.MethodError = -1 }, false},
		"negative userspace error": {func(c *Config, _ *FullConfig) { c.UserspaceError = -1 }, false},
	} {
		t.Run(name, func(t *testing.T) {
			var dialed, listened bool
			cfg, fc := DefaultConfig(netip.AddrPort{}), preflightConfig(&dialed, &listened)
			tc.edit(&cfg, &fc)
			_, err := RunFull(t.Context(), cfg, fc)
			if err == nil || dialed || listened {
				t.Fatalf("error=%v dialed=%v listened=%v", err, dialed, listened)
			}
			var ce *client.Error
			if isSetup := errors.As(err, &ce) && ce.Stage == client.StageSetup; isSetup != tc.setup {
				t.Errorf("error %v: setup stage %v, want %v", err, isSetup, tc.setup)
			}
		})
	}
}

func TestFullPreflightAccepts(t *testing.T) {
	var dialed, listened bool
	cfg, fc := DefaultConfig(netip.AddrPort{}), preflightConfig(&dialed, &listened)
	fc.Modes, fc.KeyID, fc.Passphrase = wire.ModeEncrypted|wire.ModeUnauthenticated, "test", []byte("test secret")
	fc.Features = client.FeatureModes
	if _, err := RunFull(t.Context(), cfg, fc); !errors.Is(err, errDialed) || !dialed {
		t.Fatalf("error=%v dialed=%v, want the dial error", err, dialed)
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
