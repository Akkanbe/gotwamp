package gotwamp

import "fmt"

// Timestamping is a timestamping method.
type Timestamping uint8

const (
	// TimestampAuto tries TimestampKernel and falls back to TimestampUserspace.
	TimestampAuto Timestamping = iota
	// TimestampKernel takes receive timestamps from the kernel (SO_TIMESTAMPING,
	// or SO_TIMESTAMPNS when SO_TIMESTAMPING is refused).
	TimestampKernel
	// TimestampUserspace takes time.Now() right after recvmsg and right before
	// sendmsg.
	TimestampUserspace
	// TimestampHardware takes receive timestamps, and transmit timestamps with
	// transmit timestamping, from the network card (SO_TIMESTAMPING with
	// SOF_TIMESTAMPING_RX_HARDWARE / TX_HARDWARE / RAW_HARDWARE), and the
	// kernel's software timestamps where the card gives none. It turns the
	// card's timestamping on (SIOCSHWTSTAMP, which needs CAP_NET_ADMIN) when
	// it is off, and falls back to TimestampKernel when the card cannot
	// timestamp every packet; results report the actual mode in effect.
	TimestampHardware
)

// String returns the configuration name of the mode.
func (m Timestamping) String() string {
	switch m {
	case TimestampAuto:
		return "auto"
	case TimestampKernel:
		return "kernel"
	case TimestampUserspace:
		return "userspace"
	case TimestampHardware:
		return "hardware"
	default:
		return fmt.Sprintf("Mode(%d)", uint8(m))
	}
}
