//go:build !unix

//declscope:namespace sender

package sender

import (
	"errors"
	"syscall"
)

// setTOS is not supported off unix; gotwamp targets Linux (SPEC §2.2).
func setTOS(syscall.RawConn, int, bool) error {
	return errors.New("control dscp is not supported on this platform")
}
