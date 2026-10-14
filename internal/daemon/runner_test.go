package daemon

import (
	"net/netip"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// TestOpenLightTxTimestamps: the socket the daemon opens for a Light run
// asks for kernel transmit timestamps (T1', SPEC §8.2) unless the run is
// in user space.
func TestOpenLightTxTimestamps(t *testing.T) {
	target := netip.MustParseAddrPort("127.0.0.1:862")
	p := &testPlan{senderPorts: config.PortRange{First: 49152, Last: 57343}}
	for _, mode := range []tstamp.Mode{tstamp.ModeAuto, tstamp.ModeKernel, tstamp.ModeUserspace} {
		cfg := sender.DefaultConfig(target)
		cfg.Timestamping = mode
		c, err := (&runner{}).openLight(p, target, cfg)
		if err != nil {
			t.Logf("%v: %v", mode, err)
			continue
		}
		tx := c.(tstamp.TxTimestamper).TxTimestamps()
		if want := c.Mode() == tstamp.ModeKernel; tx != want {
			t.Errorf("%v: in %v, TxTimestamps() = %v, want %v", mode, c.Mode(), tx, want)
		}
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}
}
