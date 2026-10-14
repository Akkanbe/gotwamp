package tstamp

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// netInterfaces is net.Interfaces. Tests replace it.
var netInterfaces = net.Interfaces

// RouteInterface returns the name of the network interface that packets to
// dst leave through, for Options.Interface of a socket bound to the wildcard
// address. It asks the routing table by connecting a UDP socket, which sends
// nothing.
func RouteInterface(dst netip.Addr) (string, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(dst, 9)))
	if err != nil {
		return "", fmt.Errorf("tstamp: route to %s: %w", dst, err)
	}
	local := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr()
	_ = c.Close()
	return interfaceOf(local)
}

// interfaceOf returns the name of the interface that has addr. An IPv6
// address with a zone names the interface itself.
//
//declscope:package // Listen finds the card to set up in hardware mode with it
func interfaceOf(addr netip.Addr) (string, error) {
	if zone := addr.Zone(); zone != "" {
		if i, err := strconv.Atoi(zone); err == nil {
			ifi, err := net.InterfaceByIndex(i)
			if err != nil {
				return "", fmt.Errorf("tstamp: interface of %s: %w", addr, err)
			}
			return ifi.Name, nil
		}
		return zone, nil
	}
	addr = addr.Unmap()
	ifs, err := netInterfaces()
	if err != nil {
		return "", fmt.Errorf("tstamp: list interfaces: %w", err)
	}
	for _, ifi := range ifs {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Unmap() == addr {
				return ifi.Name, nil
			}
		}
	}
	return "", fmt.Errorf("tstamp: no interface has %s", addr)
}
