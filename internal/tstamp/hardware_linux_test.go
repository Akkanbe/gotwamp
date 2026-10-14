//go:build linux

//declscope:namespace kernel

package tstamp

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fakeCard simulates a card's timestamping ioctls.
type fakeCard struct {
	info    unix.EthtoolTsInfo
	infoErr error
	cur     unix.HwTstampConfig
	getErr  error
	setErr  error
	// applied overrides what the driver writes back on a set.
	applied *unix.HwTstampConfig
	sets    []unix.HwTstampConfig
	ifnames []string
}

// capable is a card that timestamps everything, with its timestamping off.
func capable() *fakeCard {
	return &fakeCard{info: unix.EthtoolTsInfo{
		So_timestamping: rxHardware | txHardware | unix.SOF_TIMESTAMPING_SOFTWARE,
		Phc_index:       2,
		Tx_types:        1<<unix.HWTSTAMP_TX_OFF | 1<<unix.HWTSTAMP_TX_ON,
		Rx_filters:      1<<unix.HWTSTAMP_FILTER_NONE | 1<<unix.HWTSTAMP_FILTER_ALL,
	}}
}

// install replaces the ioctls with the fake for the duration of the test.
func (c *fakeCard) install(t *testing.T) {
	t.Helper()
	gi, gh, sh := getTsInfo, getHwTstamp, setHwTstamp
	getTsInfo = func(_ int, ifname string) (*unix.EthtoolTsInfo, error) {
		c.ifnames = append(c.ifnames, ifname)
		info := c.info
		return &info, c.infoErr
	}
	getHwTstamp = func(int, string) (*unix.HwTstampConfig, error) {
		cur := c.cur
		return &cur, c.getErr
	}
	setHwTstamp = func(_ int, _ string, cfg *unix.HwTstampConfig) error {
		c.sets = append(c.sets, *cfg)
		if c.setErr != nil {
			return c.setErr
		}
		if c.applied != nil {
			*cfg = *c.applied
		}
		c.cur = *cfg
		return nil
	}
	t.Cleanup(func() { getTsInfo, getHwTstamp, setHwTstamp = gi, gh, sh })
}

func TestEnableCard(t *testing.T) {
	on := unix.HwTstampConfig{Tx_type: unix.HWTSTAMP_TX_ON, Rx_filter: unix.HWTSTAMP_FILTER_ALL}
	cases := []struct {
		name    string
		card    func(c *fakeCard)
		tx      bool
		sets    []unix.HwTstampConfig
		wantErr string
	}{
		{"turned on", func(*fakeCard) {}, true, []unix.HwTstampConfig{on}, ""},
		{"already on", func(c *fakeCard) { c.cur = on }, true, nil, ""},
		{
			"receive only keeps transmit off", func(*fakeCard) {}, false,
			[]unix.HwTstampConfig{{Tx_type: unix.HWTSTAMP_TX_OFF, Rx_filter: unix.HWTSTAMP_FILTER_ALL}},
			"",
		},
		{"receive only on a card already receiving", func(c *fakeCard) {
			c.cur.Rx_filter = unix.HWTSTAMP_FILTER_ALL
		}, false, nil, ""},
		{"PTP filter widened, one-step kept", func(c *fakeCard) {
			c.cur = unix.HwTstampConfig{Tx_type: unix.HWTSTAMP_TX_ONESTEP_SYNC, Rx_filter: unix.HWTSTAMP_FILTER_PTP_V2_EVENT}
		}, true, []unix.HwTstampConfig{{Tx_type: unix.HWTSTAMP_TX_ONESTEP_SYNC, Rx_filter: unix.HWTSTAMP_FILTER_ALL}}, ""},
		{"current setting unknown", func(c *fakeCard) { c.getErr = unix.EOPNOTSUPP }, false, []unix.HwTstampConfig{on}, ""},
		{"no permission", func(c *fakeCard) { c.setErr = unix.EPERM }, true, []unix.HwTstampConfig{on}, "CAP_NET_ADMIN"},
		{"set refused", func(c *fakeCard) { c.setErr = unix.ERANGE }, true, []unix.HwTstampConfig{on}, "SIOCSHWTSTAMP"},
		{"driver narrowed the filter", func(c *fakeCard) {
			c.applied = &unix.HwTstampConfig{Tx_type: unix.HWTSTAMP_TX_ON, Rx_filter: unix.HWTSTAMP_FILTER_PTP_V2_EVENT}
		}, true, []unix.HwTstampConfig{on}, "applied receive filter"},
		{"driver left transmit off", func(c *fakeCard) {
			c.applied = &unix.HwTstampConfig{Tx_type: unix.HWTSTAMP_TX_OFF, Rx_filter: unix.HWTSTAMP_FILTER_ALL}
		}, true, []unix.HwTstampConfig{on}, "transmit type"},
		{"no ethtool", func(c *fakeCard) { c.infoErr = unix.EOPNOTSUPP }, true, nil, "ETHTOOL_GET_TS_INFO"},
		{"software only", func(c *fakeCard) { c.info.So_timestamping = unix.SOF_TIMESTAMPING_SOFTWARE }, false, nil, "no receive timestamps"},
		{"PTP filters only", func(c *fakeCard) {
			c.info.Rx_filters = 1 << unix.HWTSTAMP_FILTER_PTP_V2_EVENT
		}, false, nil, "HWTSTAMP_FILTER_ALL"},
		{"no transmit timestamps", func(c *fakeCard) { c.info.Tx_types = 1 << unix.HWTSTAMP_TX_OFF }, true, nil, "no transmit timestamps"},
		{"no transmit needed", func(c *fakeCard) {
			c.info.So_timestamping &^= txHardware
			c.cur.Rx_filter = unix.HWTSTAMP_FILTER_ALL
		}, false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := capable()
			tc.card(c)
			c.install(t)
			phc, err := enableCard(-1, "eth9", tc.tx)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("enableCard: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("enableCard: %v, want an error with %q", err, tc.wantErr)
			}
			if err == nil && phc != 2 {
				t.Errorf("PHC %d, want 2", phc)
			}
			if len(c.sets) != len(tc.sets) {
				t.Fatalf("sets %+v, want %+v", c.sets, tc.sets)
			}
			for i := range c.sets {
				if c.sets[i] != tc.sets[i] {
					t.Errorf("set %d: %+v, want %+v", i, c.sets[i], tc.sets[i])
				}
			}
		})
	}
}

func TestHardwareMode(t *testing.T) {
	c := capable()
	c.install(t)
	conn, err := listen(t, Options{Mode: ModeHardware, TxTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	hw := conn.(HardwareReporter).Hardware()
	if conn.Mode() != ModeHardware || hw.Interface != "lo" || hw.PHC != 2 || hw.Err != nil {
		t.Fatalf("mode %v, hardware %+v", conn.Mode(), hw)
	}
	if len(c.ifnames) != 1 || c.ifnames[0] != "lo" {
		t.Errorf("card set up on %v, want lo", c.ifnames)
	}
	if !conn.(TxTimestamper).TxTimestamps() {
		t.Error("transmit timestamps off")
	}
	// The loopback device takes no hardware timestamps: the kernel's
	// software ones stand in, per packet.
	meta := received(t, conn)
	if !meta.Kernel || meta.Hardware || meta.HardwareRejected {
		t.Errorf("meta %+v, want a software timestamp", meta)
	}
	tx := conn.(TxTimestamper)
	user, id, err := tx.WriteToID([]byte{1}, conn.LocalAddr(), -1)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var got bool
	for !got && time.Now().Before(deadline) {
		if _, err := tx.ReadTxTimestamps(func(i uint32, at time.Time) {
			got = got || i == id && !at.Before(user)
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if !got {
		t.Error("no transmit timestamp")
	}
}

func TestHardwareInterface(t *testing.T) {
	c := capable()
	c.install(t)
	conn, err := Listen("udp4", netip.MustParseAddrPort("0.0.0.0:0"), Options{Mode: ModeHardware, Interface: "eth7"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if hw := conn.(HardwareReporter).Hardware(); conn.Mode() != ModeHardware || hw.Interface != "eth7" || hw.Err != nil {
		t.Fatalf("mode %v, hardware %+v", conn.Mode(), hw)
	}
	if conn.(TxTimestamper).TxTimestamps() {
		t.Error("transmit timestamps on without Options.TxTimestamps")
	}
}

func TestHardwareFallback(t *testing.T) {
	t.Run("loopback card", func(t *testing.T) {
		// The real ioctls: lo has software timestamps only.
		conn, err := listen(t, Options{Mode: ModeHardware, TxTimestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		hw := conn.(HardwareReporter).Hardware()
		if conn.Mode() != ModeKernel || hw.Interface != "lo" || hw.Err == nil {
			t.Fatalf("mode %v, hardware %+v", conn.Mode(), hw)
		}
		if !conn.(TxTimestamper).TxTimestamps() || !received(t, conn).Kernel {
			t.Error("kernel timestamps off after the fallback")
		}
	})
	t.Run("wildcard", func(t *testing.T) {
		capable().install(t)
		conn, err := Listen("udp6", netip.MustParseAddrPort("[::]:0"), Options{Mode: ModeHardware})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		if hw := conn.(HardwareReporter).Hardware(); conn.Mode() != ModeKernel || !errors.Is(hw.Err, errWildcard) {
			t.Fatalf("mode %v, hardware %+v", conn.Mode(), hw)
		}
	})
	t.Run("hardware flags refused", func(t *testing.T) {
		capable().install(t)
		refuse(t, func(opt, value int) bool { return opt == unix.SO_TIMESTAMPING && value&rxHardware != 0 })
		conn, err := listen(t, Options{Mode: ModeHardware, TxTimestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		hw := conn.(HardwareReporter).Hardware()
		if conn.Mode() != ModeKernel || hw.Err == nil || !conn.(TxTimestamper).TxTimestamps() {
			t.Fatalf("mode %v, hardware %+v", conn.Mode(), hw)
		}
	})
	t.Run("transmit refused with hardware", func(t *testing.T) {
		capable().install(t)
		refuse(t, func(opt, value int) bool { return opt == unix.SO_TIMESTAMPING && value&txHardware != 0 })
		conn, err := listen(t, Options{Mode: ModeHardware, TxTimestamps: true})
		if err != nil {
			t.Fatal(err)
		}
		// Hardware receive timestamps win over software transmit ones.
		if conn.Mode() != ModeHardware || conn.(TxTimestamper).TxTimestamps() {
			t.Fatalf("mode %v, transmit timestamps %v", conn.Mode(), conn.(TxTimestamper).TxTimestamps())
		}
	})
	t.Run("no kernel timestamps", func(t *testing.T) {
		capable().install(t)
		refuse(t, func(opt, _ int) bool { return opt == unix.SO_TIMESTAMPING || opt == unix.SO_TIMESTAMPNS })
		if _, err := listen(t, Options{Mode: ModeHardware}); err == nil {
			t.Fatal("Listen succeeded without kernel timestamps")
		}
	})
}

func TestNotHardware(t *testing.T) {
	conn, err := listen(t, Options{Mode: ModeKernel})
	if err != nil {
		t.Fatal(err)
	}
	if hw := conn.(HardwareReporter).Hardware(); hw != (HardwareStatus{}) {
		t.Errorf("hardware %+v, want the zero status", hw)
	}
}

func TestModeString(t *testing.T) {
	for m, want := range map[Mode]string{
		ModeAuto: "auto", ModeKernel: "kernel", ModeUserspace: "userspace", ModeHardware: "hardware", ModeHardware + 1: "Mode(4)",
	} {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", uint8(m), got, want)
		}
	}
}
