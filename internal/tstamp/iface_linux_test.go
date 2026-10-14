//go:build linux

package tstamp

import (
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"testing"
)

func TestRouteInterface(t *testing.T) {
	name, err := RouteInterface(netip.MustParseAddr("127.0.0.1"))
	if err != nil || name != "lo" {
		t.Fatalf("RouteInterface(127.0.0.1) = %q, %v; want lo", name, err)
	}
}

func TestInterfaceOf(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip(err)
	}
	cases := []struct {
		addr string
		want string
		ok   bool
	}{
		{"127.0.0.1", "lo", true},
		{"::ffff:127.0.0.1", "lo", true},
		{"fe80::1%" + strconv.Itoa(lo.Index), "lo", true},
		{"fe80::1%eth5", "eth5", true},
		{"fe80::1%999999", "", false},
		{"192.0.2.250", "", false},
	}
	for _, tc := range cases {
		got, err := interfaceOf(netip.MustParseAddr(tc.addr))
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("interfaceOf(%s) = %q, %v", tc.addr, got, err)
		}
	}
}

func TestInterfaceOfListError(t *testing.T) {
	orig := netInterfaces
	netInterfaces = func() ([]net.Interface, error) { return nil, syscall.EACCES }
	t.Cleanup(func() { netInterfaces = orig })
	if _, err := interfaceOf(netip.MustParseAddr("127.0.0.1")); err == nil {
		t.Error("interfaceOf succeeded without the interface list")
	}
}
