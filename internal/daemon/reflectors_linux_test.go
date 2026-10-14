//go:build linux

//declscope:namespace reflectors // tests of the control listener

package daemon

import (
	"net"
	"net/netip"
	"syscall"
	"testing"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestControlKeepAlive: an accepted TWAMP-Control connection has TCP
// keepalive at 120 s idle, 5 s interval, 6 probes (SPEC §5.1).
func TestControlKeepAlive(t *testing.T) {
	ln, err := listenControl("tcp4", netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := a.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	err = raw.Control(func(fd uintptr) {
		for name, opt := range map[string][2]int{
			"SO_KEEPALIVE":  {syscall.SOL_SOCKET, syscall.SO_KEEPALIVE},
			"TCP_KEEPIDLE":  {syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE},
			"TCP_KEEPINTVL": {syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL},
			"TCP_KEEPCNT":   {syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT},
		} {
			v, err := syscall.GetsockoptInt(int(fd), opt[0], opt[1])
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			got[name] = v
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["SO_KEEPALIVE"] != 1 || got["TCP_KEEPIDLE"] != 120 || got["TCP_KEEPINTVL"] != 5 || got["TCP_KEEPCNT"] != 6 {
		t.Errorf("keepalive = %v, want on, 120 s, 5 s, 6", got)
	}
}

func TestSessionSecurity(t *testing.T) {
	for m, want := range map[wire.SecurityMode]gotwampv1.SecurityMode{
		wire.SecurityUnauthenticated: gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED,
		wire.SecurityAuthenticated:   gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED,
		wire.SecurityEncrypted:       gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED,
		wire.SecurityMixed:           gotwampv1.SecurityMode_SECURITY_MODE_MIXED,
		0:                            gotwampv1.SecurityMode_SECURITY_MODE_UNSPECIFIED,
	} {
		if got := sessionSecurity(m); got != want {
			t.Errorf("sessionSecurity(%d) = %v, want %v", m, got, want)
		}
	}
}
