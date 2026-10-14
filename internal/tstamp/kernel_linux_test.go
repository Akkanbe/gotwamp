//go:build linux

package tstamp

import (
	"errors"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// refuse makes setsockopt fail for the options reject selects, as a kernel
// without them would.
func refuse(t *testing.T, reject func(opt, value int) bool) {
	t.Helper()
	orig := setsockoptInt
	setsockoptInt = func(fd, level, opt, value int) error {
		if level == unix.SOL_SOCKET && reject(opt, value) {
			return syscall.ENOPROTOOPT
		}
		return orig(fd, level, opt, value)
	}
	t.Cleanup(func() { setsockoptInt = orig })
}

func listen(t *testing.T, opts Options) (Conn, error) {
	t.Helper()
	c, err := Listen("udp4", netip.MustParseAddrPort("127.0.0.1:0"), opts)
	if err == nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

// received sends one datagram to c and returns its metadata.
func received(t *testing.T, c Conn) RxMeta {
	t.Helper()
	peer, err := listen(t, Options{Mode: ModeUserspace})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.WriteTo([]byte{1}, c.LocalAddr(), -1); err != nil {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, meta, err := c.ReadFrom(make([]byte, 8))
	if err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestFallbackToTimestampNS(t *testing.T) {
	refuse(t, func(opt, _ int) bool { return opt == unix.SO_TIMESTAMPING })
	c, err := listen(t, Options{Mode: ModeKernel, TxTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode() != ModeKernel || c.(TxTimestamper).TxTimestamps() {
		t.Errorf("mode %v, transmit timestamps %v", c.Mode(), c.(TxTimestamper).TxTimestamps())
	}
	if meta := received(t, c); !meta.Kernel {
		t.Error("no SCM_TIMESTAMPNS receive timestamp")
	}
}

func TestFallbackWithoutTx(t *testing.T) {
	refuse(t, func(opt, value int) bool {
		return opt == unix.SO_TIMESTAMPING && value&unix.SOF_TIMESTAMPING_TX_SOFTWARE != 0
	})
	c, err := listen(t, Options{Mode: ModeAuto, TxTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode() != ModeKernel || c.(TxTimestamper).TxTimestamps() {
		t.Errorf("mode %v, transmit timestamps %v", c.Mode(), c.(TxTimestamper).TxTimestamps())
	}
	if meta := received(t, c); !meta.Kernel {
		t.Error("no SCM_TIMESTAMPING receive timestamp")
	}
}

func TestFallbackToUserspace(t *testing.T) {
	refuse(t, func(opt, _ int) bool { return opt == unix.SO_TIMESTAMPING || opt == unix.SO_TIMESTAMPNS })
	c, err := listen(t, Options{Mode: ModeAuto, TxTimestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode() != ModeUserspace || c.(TxTimestamper).TxTimestamps() {
		t.Errorf("mode %v", c.Mode())
	}
	if meta := received(t, c); meta.Kernel || meta.At.IsZero() {
		t.Errorf("user-space meta %+v", meta)
	}

	_, err = listen(t, Options{Mode: ModeKernel})
	if !errors.Is(err, syscall.ENOPROTOOPT) {
		t.Errorf("Listen(ModeKernel) error = %v", err)
	}
}
