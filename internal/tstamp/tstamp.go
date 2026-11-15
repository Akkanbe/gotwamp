// Package tstamp is the UDP socket layer of TWAMP-Test: it sends and receives
// datagrams with the send and receive timestamps, the TTL / Hop Limit and the
// TOS / Traffic Class that the measurement needs (SPEC §6.1, §8).
//
// Receive timestamps come from the network card (ModeHardware), from the
// kernel (ModeKernel: SO_TIMESTAMPING software timestamps, or SO_TIMESTAMPNS
// where SO_TIMESTAMPING is refused) or from time.Now() right after recvmsg
// (ModeUserspace). Send times are always taken right before sendmsg, since T1
// and T3 go into the packet; a Conn opened with Options.TxTimestamps also
// reports the transmit time of every packet afterwards, from the card or the
// kernel, through TxTimestamper (SPEC §8.1, §8.2).
//
// Hardware timestamps are read from the card's PTP hardware clock (PHC) as
// they are: the package assumes that the PHC is synchronized to
// CLOCK_REALTIME (phc2sys, SPEC §8.1) and does not correct an offset. As a
// guard, a hardware receive timestamp that differs from the kernel's software
// timestamp of the same datagram by more than a millisecond is replaced by
// the software one (RxMeta.HardwareRejected).
//
// The package is Linux only; on other systems Listen returns ErrUnsupported.
package tstamp

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// ErrUnsupported is returned when the requested timestamping mode, or the
// package itself, is not available. It matches errors.ErrUnsupported.
//
//declscope:ignore overexported // callers match errors.ErrUnsupported, which it wraps; conn_other.go returns it
var ErrUnsupported = fmt.Errorf("tstamp: %w", errors.ErrUnsupported)

// Mode is a timestamping method.
type Mode uint8

const (
	// ModeAuto tries ModeKernel and falls back to ModeUserspace.
	ModeAuto Mode = iota
	// ModeKernel takes receive timestamps from the kernel (SO_TIMESTAMPING,
	// or SO_TIMESTAMPNS when SO_TIMESTAMPING is refused).
	ModeKernel
	// ModeUserspace takes time.Now() right after recvmsg and right before
	// sendmsg.
	ModeUserspace
	// ModeHardware takes receive timestamps, and transmit timestamps with
	// Options.TxTimestamps, from the network card (SO_TIMESTAMPING with
	// SOF_TIMESTAMPING_RX_HARDWARE / TX_HARDWARE / RAW_HARDWARE), and the
	// kernel's software timestamps where the card gives none. It turns the
	// card's timestamping on (SIOCSHWTSTAMP, which needs CAP_NET_ADMIN) when
	// it is off, and falls back to ModeKernel when the card cannot
	// timestamp every packet; HardwareReporter tells why.
	ModeHardware
)

// String returns the configuration name of the mode.
func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeKernel:
		return "kernel"
	case ModeUserspace:
		return "userspace"
	case ModeHardware:
		return "hardware"
	default:
		return fmt.Sprintf("Mode(%d)", uint8(m))
	}
}

// Options configures a Conn.
type Options struct {
	// TTL is the TTL / Hop Limit of sent packets, 1-255. Zero means 255.
	TTL int
	// TOS is the socket default IP_TOS / IPV6_TCLASS (DSCP<<2 | ECN), 0-255.
	// Zero leaves the socket default untouched.
	TOS int
	// Mode is the timestamping method to use.
	Mode Mode
	// ReadBuffer is SO_RCVBUF in octets. Zero means 1 MiB.
	ReadBuffer int
	// TxTimestamps asks for transmit timestamps (SO_TIMESTAMPING with
	// OPT_ID and OPT_TSONLY), read through TxTimestamper. It takes effect
	// only in kernel and hardware mode. The timestamps queue on the socket's
	// error queue and use its receive buffer until read, so a Conn opened
	// with it must call ReadTxTimestamps regularly.
	TxTimestamps bool
	// Interface names the network interface whose card timestamps the
	// packets in ModeHardware. Empty means the interface that has the bound
	// address; a socket bound to the wildcard address needs it (see
	// RouteInterface). Packets that arrive on another interface carry
	// software timestamps.
	Interface string
}

// RxMeta is what the kernel reported about a received datagram.
type RxMeta struct {
	// At is the receive timestamp (CLOCK_REALTIME, no monotonic reading).
	At time.Time
	// TTL is the received TTL / Hop Limit, or -1 when unavailable.
	TTL int
	// TOS is the received TOS / Traffic Class, or -1 when unavailable.
	TOS int
	// Kernel reports that At is the kernel's receive timestamp, its software
	// one or the card's. It is false in user-space mode, and when the kernel
	// attached no timestamp.
	Kernel bool
	// Hardware reports that At is the network card's receive timestamp
	// (ModeHardware). Kernel is then true as well.
	Hardware bool
	// HardwareRejected reports that the card's timestamp was not used
	// because it differed from the kernel's software timestamp by more than
	// a millisecond (or lay that far in the future): a sign that the card's
	// clock is not synchronized to CLOCK_REALTIME. At is the software
	// timestamp then.
	HardwareRejected bool
	// Dst is the destination address of the datagram (IP_PKTINFO /
	// IPV6_PKTINFO), which tells a socket bound to the wildcard address the
	// address it was reached at. It is the zero Addr when unavailable.
	Dst netip.Addr
	// IfIndex is the index of the interface the datagram arrived on, or 0.
	IfIndex int
}

// Conn is an unconnected UDP socket that timestamps every datagram.
//
// ReadFrom and WriteTo may be called concurrently, from any number of
// goroutines.
type Conn interface {
	// WriteTo sends b to to and returns the time taken right before sendmsg.
	// A negative tos uses the socket default; 0-255 sets the TOS / Traffic
	// Class of this packet only (per-packet cmsg).
	WriteTo(b []byte, to netip.AddrPort, tos int) (at time.Time, err error)
	// ReadFrom receives one datagram into b. A datagram longer than b is
	// truncated to len(b); size b one octet beyond the largest acceptable
	// payload to detect that. When the read deadline passes, the error
	// matches os.ErrDeadlineExceeded.
	ReadFrom(b []byte) (n int, from netip.AddrPort, meta RxMeta, err error)
	// LocalAddr returns the bound address and port.
	LocalAddr() netip.AddrPort
	// SetReadDeadline sets the deadline of ReadFrom. The zero time clears it.
	SetReadDeadline(t time.Time) error
	// Mode returns the timestamping method in effect: ModeHardware,
	// ModeKernel or ModeUserspace, never ModeAuto.
	Mode() Mode
	// Close closes the socket. A blocked ReadFrom returns net.ErrClosed.
	Close() error
}

// SourceWriter is implemented by every Conn of Listen. It is separate from
// Conn so that other implementations of Conn need not provide it.
type SourceWriter interface {
	// WriteToFrom is WriteTo with the source address of the packet set to
	// from (IP_PKTINFO / IPV6_PKTINFO), so that a socket bound to the
	// wildcard address answers from the address it was reached at
	// (RxMeta.Dst, SPEC §6.1). from must be an address of this host, of the
	// socket's family; the zero Addr lets the route choose, as WriteTo does.
	WriteToFrom(b []byte, to netip.AddrPort, from netip.Addr, tos int) (at time.Time, err error)
}

// TxTimestamper is implemented by every Conn of Listen; its methods work when
// the Conn was opened with Options.TxTimestamps and runs in kernel or
// hardware mode (TxTimestamps reports true), and return ErrUnsupported
// otherwise. In hardware mode a packet's timestamp is the card's, or the
// kernel's software one when the card did not take it. It is separate from
// Conn so that other implementations of Conn need not provide it.
type TxTimestamper interface {
	// TxTimestamps reports whether kernel transmit timestamps are enabled.
	TxTimestamps() bool
	// WriteToID is WriteTo that also returns the identifier the kernel
	// gives the packet's transmit timestamp (SOF_TIMESTAMPING_OPT_ID): a
	// counter of the packets sent on the socket, from 0. A failed send
	// that the kernel counted anyway (ENOBUFS) also consumes an
	// identifier.
	WriteToID(b []byte, to netip.AddrPort, tos int) (at time.Time, id uint32, err error)
	// ReadTxTimestamps reads the transmit timestamps queued so far without
	// waiting, calls fn for each, and returns how many it read. The packet
	// identifier is the one WriteToID returned.
	ReadTxTimestamps(fn func(id uint32, at time.Time)) (int, error)
}

// HardwareReporter is implemented by every Conn of Listen. It is separate
// from Conn so that other implementations of Conn need not provide it.
type HardwareReporter interface {
	// Hardware reports on hardware timestamping. It is the zero
	// HardwareStatus unless the Conn was opened in ModeHardware.
	Hardware() HardwareStatus
}

// HardwareStatus describes the hardware timestamping of a Conn opened in
// ModeHardware.
type HardwareStatus struct {
	// Interface is the interface whose card timestamps the packets, or
	// empty when none was found.
	Interface string
	// PHC is the index of the card's PTP hardware clock (/dev/ptpN), or -1
	// when unknown. It is the clock that phc2sys must keep synchronized to
	// CLOCK_REALTIME.
	PHC int
	// Err is why the Conn fell back to ModeKernel; nil when hardware
	// timestamping is in effect.
	Err error
}
