//go:build unix

//declscope:namespace sender

package sender

import "syscall"

// setTOS sets the TOS (IPv4) or Traffic Class (IPv6) of a socket.
func setTOS(rc syscall.RawConn, tos int, v6 bool) error {
	var serr error
	err := rc.Control(func(fd uintptr) {
		if v6 {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_TCLASS, tos) //nolint:gosec // a socket descriptor fits in int
			return
		}
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, tos) //nolint:gosec // a socket descriptor fits in int
	})
	if err != nil {
		return err
	}
	return serr
}
