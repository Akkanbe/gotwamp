//go:build !linux

package tstamp

import "net/netip"

// Listen is not supported outside Linux.
func Listen(_ string, _ netip.AddrPort, _ Options) (Conn, error) {
	return nil, ErrUnsupported
}
