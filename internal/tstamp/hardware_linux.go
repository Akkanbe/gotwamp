//go:build linux

// Hardware timestamps are SO_TIMESTAMPING with the card's flags added: the
// same device as kernel_linux.go.
//
//declscope:namespace kernel

package tstamp

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// The ioctls that read and set up a card's timestamping. Tests replace them
// to simulate a card that timestamps.
var (
	getTsInfo   = unix.IoctlGetEthtoolTsInfo
	getHwTstamp = unix.IoctlGetHwTstamp
	setHwTstamp = unix.IoctlSetHwTstamp
)

const (
	// rxHardware asks for the card's receive timestamps, reported in ts[2]
	// of SCM_TIMESTAMPING as raw PHC time.
	rxHardware = unix.SOF_TIMESTAMPING_RX_HARDWARE | unix.SOF_TIMESTAMPING_RAW_HARDWARE
	// txHardware asks for the card's transmit timestamps. Without
	// SOF_TIMESTAMPING_OPT_TX_SWHW the kernel skips the software one of a
	// packet the card takes, so each packet gets one timestamp (SPEC §8.2).
	txHardware = unix.SOF_TIMESTAMPING_TX_HARDWARE
	// hardwareTolerance is the largest difference between the card's and
	// the kernel's receive timestamps of one datagram that is taken as the
	// card's clock agreeing with CLOCK_REALTIME. The two are microseconds
	// apart; an unsynchronized PHC, or one running on TAI, is off by much
	// more.
	hardwareTolerance = time.Millisecond
)

// errWildcard is the reason hardware mode falls back on a socket bound to the
// wildcard address without Options.Interface.
//
//declscope:package // Listen reports it
var errWildcard = errors.New("a socket bound to the wildcard address needs Options.Interface")

// enableCard makes the card of ifname timestamp every received packet, and
// every transmitted one when tx is set, and returns the index of its PHC. It
// reads the card's configuration first and changes it (SIOCSHWTSTAMP, which
// needs CAP_NET_ADMIN) only when it lacks something. A change only widens the
// configuration (receive filter HWTSTAMP_FILTER_ALL, transmit on), so that a
// PTP daemon on the same card keeps working, and it is left in place on
// close, as other sockets may rely on it.
//
//declscope:package // Listen sets up the card in hardware mode with it
func enableCard(fd int, ifname string, tx bool) (phc int, err error) {
	info, err := getTsInfo(fd, ifname)
	if err != nil {
		return -1, fmt.Errorf("%s: ETHTOOL_GET_TS_INFO: %w", ifname, err)
	}
	phc = int(info.Phc_index)
	switch {
	case info.So_timestamping&rxHardware != rxHardware:
		return phc, fmt.Errorf("%s: the card gives no receive timestamps", ifname)
	case info.Rx_filters&(1<<unix.HWTSTAMP_FILTER_ALL) == 0:
		return phc, fmt.Errorf("%s: the card cannot timestamp every received packet (HWTSTAMP_FILTER_ALL)", ifname)
	case tx && (info.So_timestamping&txHardware == 0 || info.Tx_types&(1<<unix.HWTSTAMP_TX_ON) == 0):
		return phc, fmt.Errorf("%s: the card gives no transmit timestamps", ifname)
	}
	cur, gerr := getHwTstamp(fd, ifname)
	if gerr == nil && cur.Rx_filter == unix.HWTSTAMP_FILTER_ALL && (!tx || cur.Tx_type != unix.HWTSTAMP_TX_OFF) {
		return phc, nil
	}
	// Keep a transmit mode in use (one-step PTP included), and leave it off
	// when nothing needs it; turn it on when the current one is unknown.
	want := unix.HwTstampConfig{Tx_type: unix.HWTSTAMP_TX_ON, Rx_filter: unix.HWTSTAMP_FILTER_ALL}
	if gerr == nil && (cur.Tx_type != unix.HWTSTAMP_TX_OFF || !tx) {
		want.Tx_type = cur.Tx_type
	}
	if err := setHwTstamp(fd, ifname, &want); err != nil {
		if errors.Is(err, unix.EPERM) {
			return phc, fmt.Errorf("%s: SIOCSHWTSTAMP needs CAP_NET_ADMIN, or turn timestamping on beforehand (hwstamp_ctl -i %s -r 1 -t 1): %w", ifname, ifname, err)
		}
		return phc, fmt.Errorf("%s: SIOCSHWTSTAMP: %w", ifname, err)
	}
	// The driver writes back what it applied.
	if want.Rx_filter != unix.HWTSTAMP_FILTER_ALL || tx && want.Tx_type == unix.HWTSTAMP_TX_OFF {
		return phc, fmt.Errorf("%s: the card applied receive filter %d and transmit type %d", ifname, want.Rx_filter, want.Tx_type)
	}
	return phc, nil
}

// pickRxTimestamp fills the receive timestamp of meta from the software (sw)
// and hardware (hw) timestamps of SCM_TIMESTAMPING, either of which may be
// missing. The card's is used unless it disagrees with the software one, or,
// without one, lies in the future of meta.At (the user-space receive time).
//
//declscope:package // parseRxCmsgs decodes SCM_TIMESTAMPING through it
func pickRxTimestamp(meta *RxMeta, sw time.Time, swOK bool, hw time.Time, hwOK bool) {
	if hwOK {
		var ok bool
		switch {
		case swOK:
			d := hw.Sub(sw)
			ok = d <= hardwareTolerance && d >= -hardwareTolerance
		default:
			ok = meta.At.IsZero() || !hw.After(meta.At.Add(hardwareTolerance))
		}
		if ok {
			meta.At, meta.Kernel, meta.Hardware = hw, true, true
			return
		}
		meta.HardwareRejected = true
	}
	if swOK {
		meta.At, meta.Kernel = sw, true
	}
}
