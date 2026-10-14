package sender

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig(netip.MustParseAddrPort("192.0.2.10:862"))
	if cfg.Count != 100 || cfg.Interval != 100*time.Millisecond || cfg.Timeout != 2*time.Second ||
		cfg.Padding != 27 || cfg.TTL != 255 || cfg.Window != time.Minute || cfg.PaddingFill != PaddingAuto {
		t.Errorf("DefaultConfig = %+v", cfg)
	}
}

func TestNormalize(t *testing.T) {
	target := netip.MustParseAddrPort("192.0.2.10:862")
	cfg := Config{Target: netip.MustParseAddrPort("[::ffff:192.0.2.10]:862"), StatelessReflector: true}
	if err := normalize(&cfg, true); err != nil {
		t.Fatal(err)
	}
	if cfg.Target != target || cfg.Interval != defaultInterval || cfg.Timeout != DefaultTimeout ||
		cfg.Window != defaultWindow || cfg.SpinBefore != defaultSpinBefore || cfg.TTL != 255 || cfg.StatelessReflector {
		t.Errorf("normalized = %+v", cfg)
	}

	bad := []struct {
		name string
		edit func(*Config)
	}{
		{"no target", func(c *Config) { c.Target = netip.AddrPort{} }},
		{"no port", func(c *Config) { c.Target = netip.AddrPortFrom(target.Addr(), 0) }},
		{"family mismatch", func(c *Config) { c.Source = netip.MustParseAddrPort("[::1]:0") }},
		{"negative padding", func(c *Config) { c.Padding = -1 }},
		{"padding too long", func(c *Config) { c.Padding = maxPadding + 1 }},
		{"dscp", func(c *Config) { c.DSCP = 64 }},
		{"ttl", func(c *Config) { c.TTL = 256 }},
		{"padding fill", func(c *Config) { c.PaddingFill = 9 }},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(target)
			tt.edit(&cfg)
			if err := normalize(&cfg, false); err == nil {
				t.Error("normalize succeeded")
			}
		})
	}
}

func TestRandomPadding(t *testing.T) {
	target := netip.MustParseAddrPort("192.0.2.10:862")
	// Without WithRand the padding comes out random.
	s := &Sender{cfg: Config{Target: target, Padding: 64, PaddingFill: PaddingRandom}}
	if err := s.fillPadding(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(s.packet[wire.SenderHeaderSize:], make([]byte, 64)) {
		t.Error("default random padding is all zero")
	}
	s.opts.rand = bytes.NewReader(nil)
	if err := s.fillPadding(); err == nil {
		t.Error("padding from an empty random source succeeded")
	}
}

// TestLoopback runs a Light session over real UDP sockets against a minimal
// stateful reflector.
func TestLoopback(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			addr := "127.0.0.1:0"
			if network == "udp6" {
				addr = "[::1]:0"
			}
			pc, err := net.ListenPacket(network, addr)
			if err != nil {
				t.Skipf("no %s loopback: %v", network, err)
			}
			defer pc.Close()
			go echo(pc)

			target := pc.LocalAddr().(*net.UDPAddr).AddrPort()
			for _, mode := range []tstamp.Mode{tstamp.ModeAuto, tstamp.ModeUserspace} {
				t.Run(mode.String(), func(t *testing.T) { loopbackRun(t, target, mode) })
			}
		})
	}
}

// loopbackRun runs 5 packets to the echo at target with the timestamping
// mode asked for. On Linux ModeAuto gives kernel receive timestamps and T1'.
func loopbackRun(t *testing.T, target netip.AddrPort, mode tstamp.Mode) {
	cfg := DefaultConfig(target)
	cfg.Count = 5
	cfg.Interval = 10 * time.Millisecond
	cfg.Timeout = time.Second
	cfg.DSCP = 46
	cfg.TTL = 64
	cfg.Timestamping = mode
	cfg.LockOSThread = true
	var fromKernel int
	hooks := Hooks{Packet: func(p PacketResult) {
		if p.T1FromKernel {
			fromKernel++
		}
	}}
	s, err := New(cfg, WithClock(func() (bool, time.Duration) { return true, time.Millisecond }), WithHooks(hooks))
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.LocalAddr().IsValid() || s.LocalAddr().Port() == 0 {
		t.Errorf("LocalAddr = %v", s.LocalAddr())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	sum, err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Sent != 5 || sum.Received != 5 || sum.Lost != 0 || sum.Aborted || sum.Local.Addr().IsUnspecified() {
		t.Errorf("summary = %+v", sum)
	}
	if sum.RTT.Count != 5 || sum.RTT.Min < 0 || sum.RTT.Max > time.Second {
		t.Errorf("RTT = %+v", sum.RTT)
	}
	// Sent with TTL 64 over loopback: no hops forward. The echo replies
	// with the system default TTL, so only its presence is checked.
	if sum.ForwardHops.Count != 5 || sum.ForwardHops.Mode != 0 || sum.ReverseHops.Count != 5 {
		t.Errorf("hops %+v %+v", sum.ForwardHops, sum.ReverseHops)
	}
	switch {
	case mode == tstamp.ModeUserspace:
		if sum.Timestamping != tstamp.ModeUserspace || sum.TxTimestamps || fromKernel != 0 {
			t.Errorf("user-space run: %v, T1' %v, %d from kernel", sum.Timestamping, sum.TxTimestamps, fromKernel)
		}
	case sum.TxTimestamps:
		// Every reply has T1' unless it came too late; none contradicts T1.
		if sum.Timestamping != tstamp.ModeKernel || sum.TxInvalid != 0 ||
			uint64(fromKernel)+sum.TxMissing != 5 || fromKernel == 0 {
			t.Errorf("kernel run: %d from kernel, %d missing, %d invalid", fromKernel, sum.TxMissing, sum.TxInvalid)
		}
	default:
		t.Logf("no kernel transmit timestamps here: mode %v", sum.Timestamping)
	}
}

// echo reflects every Session-Sender packet it reads on pc, numbering the
// replies, with the Sender TTL taken as 64.
func echo(pc net.PacketConn) {
	buf := make([]byte, 65536)
	var seq uint32
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		t2 := time.Now()
		p := wire.ReflectorPacket{Seq: seq, ReceiveTimestamp: ntptime.FromTime(t2), SenderTTL: 64}
		seq++
		m, err := p.Reflect(buf, n)
		if err != nil {
			continue
		}
		_ = wire.PutTimestamp(buf, ntptime.FromTime(time.Now()))
		_, _ = pc.WriteTo(buf[:m], from)
	}
}

func TestSTAMPConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*Config)
		full bool
		want string
	}{
		{"full", func(c *Config) { c.STAMP = true }, true, "stamp is for TWAMP Light"},
		{"dscp ecn", func(c *Config) { c.STAMP, c.DSCPECNMonitoring = true, true }, false, "exclude each other"},
		{"padding 3", func(c *Config) { c.STAMP, c.Padding = true, 3 }, false, "Extra Padding TLV header"},
		{"ssid without stamp", func(c *Config) { c.SSID = 1 }, false, "SSID needs stamp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig(netip.MustParseAddrPort("192.0.2.1:862"))
			tt.edit(&cfg)
			if err := normalize(&cfg, tt.full); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
