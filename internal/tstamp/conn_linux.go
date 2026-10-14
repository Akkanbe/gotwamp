//go:build linux

package tstamp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultTTL        = 255
	defaultReadBuffer = 1 << 20
	// rxOOBSize holds the TTL, TOS and SCM_TIMESTAMPING cmsgs.
	rxOOBSize = 256
)

// udpConn is the Linux Conn. The per-packet cmsg buffers live in the struct
// so that the hot path does not allocate.
type udpConn struct {
	c     *net.UDPConn
	v6    bool
	local netip.AddrPort
	mode  Mode
	hw    HardwareStatus

	rxMu  sync.Mutex
	rxOOB []byte

	txMu  sync.Mutex
	txOOB []byte // one IP_TOS / IPV6_TCLASS cmsg; only the data changes
	// srcOOB is a TOS cmsg followed by a PKTINFO one, for WriteToFrom.
	srcOOB    []byte
	srcTOSLen int

	// With transmit timestamps, idMu orders the sends so that the OPT_ID
	// counter of the kernel and txNext advance together.
	idMu   sync.Mutex
	txNext uint32
	eqMu   sync.Mutex
	txq    *txQueue // nil without transmit timestamps
}

// Listen opens a UDP socket bound to local. network is "udp4" or "udp6"; port
// 0 lets the kernel choose. An unspecified address binds the wildcard. The
// socket is never connected (SPEC §6.1), and a "udp6" socket is IPv6 only.
func Listen(network string, local netip.AddrPort, opts Options) (Conn, error) {
	var v6 bool
	switch network {
	case "udp4":
	case "udp6":
		v6 = true
	default:
		return nil, fmt.Errorf("tstamp: unsupported network %q", network)
	}
	ttl, rbuf, err := resolveOptions(opts)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP(network, net.UDPAddrFromAddrPort(local))
	if err != nil {
		return nil, fmt.Errorf("tstamp: listen %s %s: %w", network, local, err)
	}
	u := &udpConn{
		c:     c,
		v6:    v6,
		local: c.LocalAddr().(*net.UDPAddr).AddrPort(),
		rxOOB: make([]byte, rxOOBSize),
		txOOB: newTOSCmsg(v6),
	}
	u.srcOOB, u.srcTOSLen = newSourceCmsgs(v6)
	if err := u.setup(ttl, rbuf, opts); err != nil {
		_ = c.Close()
		return nil, err
	}
	return u, nil
}

func resolveOptions(opts Options) (ttl, rbuf int, err error) {
	ttl, rbuf = opts.TTL, opts.ReadBuffer
	if ttl == 0 {
		ttl = defaultTTL
	}
	if rbuf == 0 {
		rbuf = defaultReadBuffer
	}
	switch {
	case ttl < 1 || ttl > 255:
		return 0, 0, fmt.Errorf("tstamp: TTL %d out of range 1-255", opts.TTL)
	case opts.TOS < 0 || opts.TOS > 255:
		return 0, 0, fmt.Errorf("tstamp: TOS %d out of range 0-255", opts.TOS)
	case rbuf < 0:
		return 0, 0, fmt.Errorf("tstamp: negative read buffer %d", opts.ReadBuffer)
	case opts.Mode > ModeHardware:
		return 0, 0, fmt.Errorf("tstamp: unknown mode %v", opts.Mode)
	}
	return ttl, rbuf, nil
}

// setup applies the socket options and resolves the timestamping mode.
func (u *udpConn) setup(ttl, rbuf int, o Options) error {
	tos, mode, tx := o.TOS, o.Mode, o.TxTimestamps
	if err := u.c.SetReadBuffer(rbuf); err != nil {
		return fmt.Errorf("tstamp: SO_RCVBUF: %w", err)
	}
	rc, err := u.c.SyscallConn()
	if err != nil {
		return fmt.Errorf("tstamp: raw conn: %w", err)
	}
	type sockopt struct {
		name          string
		level, opt    int
		value         int
		onlyIfNonZero bool
	}
	opts := []sockopt{
		{"IP_TTL", unix.IPPROTO_IP, unix.IP_TTL, ttl, false},
		{"IP_TOS", unix.IPPROTO_IP, unix.IP_TOS, tos, true},
		{"IP_RECVTTL", unix.IPPROTO_IP, unix.IP_RECVTTL, 1, false},
		{"IP_RECVTOS", unix.IPPROTO_IP, unix.IP_RECVTOS, 1, false},
		{"IP_PKTINFO", unix.IPPROTO_IP, unix.IP_PKTINFO, 1, false},
		// The DF bit stays clear (SPEC §6.1): Linux sets it on UDP by default.
		{"IP_MTU_DISCOVER", unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DONT, false},
	}
	if u.v6 {
		opts = []sockopt{
			{"IPV6_UNICAST_HOPS", unix.IPPROTO_IPV6, unix.IPV6_UNICAST_HOPS, ttl, false},
			{"IPV6_TCLASS", unix.IPPROTO_IPV6, unix.IPV6_TCLASS, tos, true},
			{"IPV6_RECVHOPLIMIT", unix.IPPROTO_IPV6, unix.IPV6_RECVHOPLIMIT, 1, false},
			{"IPV6_RECVTCLASS", unix.IPPROTO_IPV6, unix.IPV6_RECVTCLASS, 1, false},
			{"IPV6_RECVPKTINFO", unix.IPPROTO_IPV6, unix.IPV6_RECVPKTINFO, 1, false},
			// Fragment at the source rather than rely on path MTU discovery.
			{"IPV6_MTU_DISCOVER", unix.IPPROTO_IPV6, unix.IPV6_MTU_DISCOVER, unix.IPV6_PMTUDISC_DONT, false},
		}
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		for _, o := range opts {
			if o.onlyIfNonZero && o.value == 0 {
				continue
			}
			if err := unix.SetsockoptInt(int(fd), o.level, o.opt, o.value); err != nil {
				serr = fmt.Errorf("tstamp: %s: %w", o.name, err)
				return
			}
		}
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		return err
	}

	u.mode = ModeUserspace
	if mode == ModeUserspace {
		return nil
	}
	card := mode == ModeHardware && u.setupCard(rc, o.Interface, tx)
	txOn, hwOn, err := enableKernelTimestamping(rc, card, tx)
	if err != nil {
		if mode != ModeAuto {
			return fmt.Errorf("tstamp: kernel timestamping: %w", err)
		}
		return nil
	}
	u.mode = ModeKernel
	if hwOn {
		u.mode = ModeHardware
	} else if card {
		u.hw.Err = errors.New("tstamp: the kernel refused SO_TIMESTAMPING with hardware flags")
	}
	if txOn {
		u.txq = newTxQueue(rc)
	}
	return nil
}

// setupCard finds the card that timestamps the socket's packets in hardware
// mode and turns its timestamping on. It reports whether it succeeded; when
// not, u.hw.Err tells why and the socket falls back to kernel timestamps.
func (u *udpConn) setupCard(rc syscall.RawConn, ifname string, tx bool) bool {
	u.hw = HardwareStatus{Interface: ifname, PHC: -1}
	if ifname == "" {
		if !u.local.Addr().IsUnspecified() {
			ifname, u.hw.Err = interfaceOf(u.local.Addr())
		} else {
			u.hw.Err = errWildcard
		}
		if u.hw.Err != nil {
			u.hw.Err = fmt.Errorf("tstamp: hardware timestamping: %w", u.hw.Err)
			return false
		}
		u.hw.Interface = ifname
	}
	cerr := rc.Control(func(fd uintptr) {
		u.hw.PHC, u.hw.Err = enableCard(int(fd), ifname, tx)
	})
	if cerr != nil {
		u.hw.Err = cerr
	}
	if u.hw.Err != nil {
		u.hw.Err = fmt.Errorf("tstamp: hardware timestamping: %w", u.hw.Err)
		return false
	}
	return true
}

func (u *udpConn) Hardware() HardwareStatus { return u.hw }

func (u *udpConn) WriteTo(b []byte, to netip.AddrPort, tos int) (time.Time, error) {
	at, _, err := u.write(b, to, netip.Addr{}, tos)
	return at, err
}

func (u *udpConn) WriteToFrom(b []byte, to netip.AddrPort, from netip.Addr, tos int) (time.Time, error) {
	at, _, err := u.write(b, to, from, tos)
	return at, err
}

func (u *udpConn) TxTimestamps() bool { return u.txq != nil }

func (u *udpConn) WriteToID(b []byte, to netip.AddrPort, tos int) (time.Time, uint32, error) {
	if u.txq == nil {
		return time.Time{}, 0, ErrUnsupported
	}
	return u.write(b, to, netip.Addr{}, tos)
}

func (u *udpConn) ReadTxTimestamps(fn func(id uint32, at time.Time)) (int, error) {
	if u.txq == nil {
		return 0, ErrUnsupported
	}
	u.eqMu.Lock()
	defer u.eqMu.Unlock()
	return u.txq.read(fn)
}

// write sends b, from the source address from when it is valid, and returns
// the user-space send time and, with transmit timestamps, the packet's
// OPT_ID identifier.
func (u *udpConn) write(b []byte, to netip.AddrPort, from netip.Addr, tos int) (time.Time, uint32, error) {
	if !u.v6 {
		to = netip.AddrPortFrom(to.Addr().Unmap(), to.Port())
		from = from.Unmap()
	}
	if tos > 255 {
		return time.Time{}, 0, fmt.Errorf("tstamp: TOS %d out of range 0-255", tos)
	}
	if from.IsValid() && from.Is4() == u.v6 {
		return time.Time{}, 0, fmt.Errorf("tstamp: source %v is not of the socket's family", from)
	}
	if u.txq != nil {
		u.idMu.Lock()
		defer u.idMu.Unlock()
	}
	var (
		at  time.Time
		err error
	)
	switch {
	case from.IsValid():
		u.txMu.Lock()
		putSourceCmsg(u.srcOOB[u.srcTOSLen:], from)
		oob := u.srcOOB[u.srcTOSLen:]
		if tos >= 0 {
			putTOSCmsg(u.srcOOB, uint8(tos))
			oob = u.srcOOB
		}
		at = time.Now().Round(0)
		_, _, err = u.c.WriteMsgUDPAddrPort(b, oob, to)
		u.txMu.Unlock()
	case tos < 0:
		at = time.Now().Round(0)
		_, _, err = u.c.WriteMsgUDPAddrPort(b, nil, to)
	default:
		u.txMu.Lock()
		putTOSCmsg(u.txOOB, uint8(tos))
		at = time.Now().Round(0)
		_, _, err = u.c.WriteMsgUDPAddrPort(b, u.txOOB, to)
		u.txMu.Unlock()
	}
	id := u.txNext
	// The kernel numbers every packet it accepted, including one a full
	// device queue then dropped (ENOBUFS); it takes the number back on the
	// earlier errors.
	if u.txq != nil && (err == nil || errors.Is(err, unix.ENOBUFS)) {
		u.txNext++
	}
	if err != nil {
		return at, id, fmt.Errorf("tstamp: send to %s: %w", to, err)
	}
	return at, id, nil
}

func (u *udpConn) ReadFrom(b []byte) (int, netip.AddrPort, RxMeta, error) {
	u.rxMu.Lock()
	defer u.rxMu.Unlock()
	n, oobn, _, from, err := u.c.ReadMsgUDPAddrPort(b, u.rxOOB)
	meta := RxMeta{At: time.Now().Round(0), TTL: -1, TOS: -1}
	if err != nil {
		// Deadline and close errors are returned unwrapped by net, as the
		// caller checks them with errors.Is.
		return 0, netip.AddrPort{}, meta, err
	}
	parseRxCmsgs(u.rxOOB[:oobn], &meta)
	if !u.v6 {
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	}
	return n, from, meta, nil
}

func (u *udpConn) LocalAddr() netip.AddrPort { return u.local }

func (u *udpConn) SetReadDeadline(t time.Time) error { return u.c.SetReadDeadline(t) }

func (u *udpConn) Mode() Mode { return u.mode }

func (u *udpConn) Close() error { return u.c.Close() }
