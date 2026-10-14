//go:build linux

package tstamp

import (
	"encoding/binary"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

// Control message layout (cmsg(3)) on Linux: a header of cmsg_len (word
// sized), cmsg_level and cmsg_type (int32 each), then the data, then padding
// to the word size. The header is already word aligned, so CMSG_DATA is at
// cmsgHdrLen. Everything here works on caller-owned buffers and never
// allocates.
const (
	cmsgHdrLen  = unix.SizeofCmsghdr
	cmsgLenSize = unix.SizeofCmsghdr - 8
	cmsgAlign   = unix.SizeofPtr
	// tosCmsgSpace is CMSG_SPACE(4): one control message with an int payload.
	tosCmsgSpace = cmsgHdrLen + (4+cmsgAlign-1)&^(cmsgAlign-1)
	// sockExtendedErrSize is sizeof(struct sock_extended_err).
	sockExtendedErrSize = 16
	// in4PktinfoSize and in6PktinfoSize are sizeof(struct in_pktinfo) and
	// sizeof(struct in6_pktinfo).
	in4PktinfoSize = 12
	in6PktinfoSize = 20
)

// newSourceCmsgs returns a buffer holding an IP_TOS / IPV6_TCLASS control
// message followed by an IP_PKTINFO / IPV6_PKTINFO one, and the length of
// the first: the whole buffer sets both, the tail the source address only.
//
//declscope:package // the send buffer Listen keeps in each Conn
func newSourceCmsgs(v6 bool) (b []byte, tosLen int) {
	var level, typ uint32 = unix.IPPROTO_IP, unix.IP_PKTINFO
	size := in4PktinfoSize
	if v6 {
		level, typ, size = unix.IPPROTO_IPV6, unix.IPV6_PKTINFO, in6PktinfoSize
	}
	tos := newTOSCmsg(v6)
	pkt := make([]byte, unix.CmsgSpace(size))
	putCmsgHeader(pkt, uint32(unix.CmsgLen(size)), level, typ) //nolint:gosec // G115: a small constant
	return append(tos, pkt...), len(tos)
}

// putSourceCmsg sets the source address of a PKTINFO control message made by
// newSourceCmsgs; the interface index stays 0 so that the route picks it.
//
//declscope:package // WriteToFrom sets the source of each packet through it
func putSourceCmsg(b []byte, from netip.Addr) {
	data := b[cmsgHdrLen:]
	if from.Is4() {
		// struct in_pktinfo: ipi_ifindex, ipi_spec_dst (the source), ipi_addr.
		a := from.As4()
		copy(data[4:8], a[:])
		return
	}
	// struct in6_pktinfo: ipi6_addr (the source), ipi6_ifindex.
	a := from.As16()
	copy(data[:16], a[:])
}

// newTOSCmsg returns a buffer holding one IP_TOS (IPv4) or IPV6_TCLASS (IPv6)
// control message with an int payload, which sendmsg accepts for both.
//
//declscope:package // the send buffer Listen keeps in each Conn
func newTOSCmsg(v6 bool) []byte {
	var level, typ uint32 = unix.IPPROTO_IP, unix.IP_TOS
	if v6 {
		level, typ = unix.IPPROTO_IPV6, unix.IPV6_TCLASS
	}
	b := make([]byte, tosCmsgSpace)
	putCmsgHeader(b, cmsgHdrLen+4, level, typ)
	return b
}

// putTOSCmsg sets the payload of a buffer made by newTOSCmsg.
//
//declscope:package // WriteTo sets the per-packet TOS through it
func putTOSCmsg(b []byte, tos uint8) {
	binary.NativeEndian.PutUint32(b[cmsgHdrLen:], uint32(tos))
}

func putCmsgHeader(b []byte, length, level, typ uint32) {
	if cmsgLenSize == 8 {
		binary.NativeEndian.PutUint64(b, uint64(length))
	} else {
		binary.NativeEndian.PutUint32(b, length)
	}
	binary.NativeEndian.PutUint32(b[cmsgLenSize:], level)
	binary.NativeEndian.PutUint32(b[cmsgLenSize+4:], typ)
}

func cmsgLen(b []byte) uint64 {
	if cmsgLenSize == 8 {
		return binary.NativeEndian.Uint64(b)
	}
	return uint64(binary.NativeEndian.Uint32(b))
}

// nextCmsg splits the first control message off oob. It reports false at
// the end of oob and on a malformed header, which ends the walk.
func nextCmsg(oob []byte) (level, typ uint32, data, rest []byte, ok bool) {
	if uint64(len(oob)) < cmsgHdrLen {
		return 0, 0, nil, nil, false
	}
	n := cmsgLen(oob)
	if n < cmsgHdrLen || n > uint64(len(oob)) {
		return 0, 0, nil, nil, false
	}
	level = binary.NativeEndian.Uint32(oob[cmsgLenSize:])
	typ = binary.NativeEndian.Uint32(oob[cmsgLenSize+4:])
	data = oob[cmsgHdrLen:n]
	next := (n + cmsgAlign - 1) &^ (cmsgAlign - 1)
	if next < uint64(len(oob)) {
		rest = oob[next:]
	}
	return level, typ, data, rest, true
}

// parseRxCmsgs fills the TTL, TOS and kernel timestamp of meta from the
// control messages of one recvmsg. Unknown messages are skipped; a malformed
// one ends the walk.
//
//declscope:package // ReadFrom decodes every received datagram's cmsgs through it
func parseRxCmsgs(oob []byte, meta *RxMeta) {
	for {
		level, typ, data, rest, ok := nextCmsg(oob)
		if !ok {
			return
		}
		oob = rest
		switch {
		case level == unix.IPPROTO_IP && typ == unix.IP_TTL,
			level == unix.IPPROTO_IPV6 && typ == unix.IPV6_HOPLIMIT:
			if v, ok := cmsgOctet(data, 4); ok {
				meta.TTL = v
			}
		case level == unix.IPPROTO_IP && typ == unix.IP_TOS:
			// IP_RECVTOS delivers a single octet.
			if v, ok := cmsgOctet(data, 1); ok {
				meta.TOS = v
			}
		case level == unix.IPPROTO_IPV6 && typ == unix.IPV6_TCLASS:
			if v, ok := cmsgOctet(data, 4); ok {
				meta.TOS = v
			}
		case level == unix.IPPROTO_IP && typ == unix.IP_PKTINFO && len(data) >= in4PktinfoSize:
			// struct in_pktinfo: ipi_addr is the header destination address.
			meta.Dst = netip.AddrFrom4([4]byte(data[8:12]))
			meta.IfIndex = int(int32(binary.NativeEndian.Uint32(data))) //nolint:gosec // G115: the kernel's signed int
		case level == unix.IPPROTO_IPV6 && typ == unix.IPV6_PKTINFO && len(data) >= in6PktinfoSize:
			meta.Dst = netip.AddrFrom16([16]byte(data[:16]))
			meta.IfIndex = int(binary.NativeEndian.Uint32(data[16:]))
		case isTimestamping(level, typ):
			// struct scm_timestamping: ts[0] is the software timestamp, ts[2]
			// the card's (raw hardware, ModeHardware only).
			sw, swOK := timespec(data, len(data)/3)
			hw, hwOK := hardwareTimespec(data)
			pickRxTimestamp(meta, sw, swOK, hw, hwOK)
		case level == unix.SOL_SOCKET && (typ == unix.SO_TIMESTAMPNS || typ == unix.SO_TIMESTAMPNS_NEW):
			if t, ok := timespec(data, len(data)); ok {
				meta.At, meta.Kernel = t, true
			}
		}
	}
}

// isTimestamping reports an SCM_TIMESTAMPING message, in its 32-bit (old)
// or 64-bit time_t (new) layout.
func isTimestamping(level, typ uint32) bool {
	return level == unix.SOL_SOCKET && (typ == unix.SO_TIMESTAMPING || typ == unix.SO_TIMESTAMPING_NEW)
}

// hardwareTimespec decodes ts[2], the raw hardware timestamp, of a struct
// scm_timestamping.
func hardwareTimespec(data []byte) (time.Time, bool) {
	size := len(data) / 3
	return timespec(data[2*size:], size)
}

// timespec decodes the first struct timespec of data, whose fields are size/2
// octets each: 16 for a 64-bit time_t, 8 for a 32-bit one. A zero timespec
// means the kernel had no timestamp of that kind.
func timespec(data []byte, size int) (time.Time, bool) {
	var sec, nsec int64
	switch {
	case size == 16 && len(data) >= 16:
		sec = int64(binary.NativeEndian.Uint64(data))      //nolint:gosec // G115: the kernel's signed time_t
		nsec = int64(binary.NativeEndian.Uint64(data[8:])) //nolint:gosec // G115: the kernel's signed long
	case size == 8 && len(data) >= 8:
		sec = int64(int32(binary.NativeEndian.Uint32(data)))      //nolint:gosec // G115: the kernel's signed time_t
		nsec = int64(int32(binary.NativeEndian.Uint32(data[4:]))) //nolint:gosec // G115: the kernel's signed long
	default:
		return time.Time{}, false
	}
	if sec == 0 && nsec == 0 || nsec < 0 || nsec >= 1e9 {
		return time.Time{}, false
	}
	return time.Unix(sec, nsec), true
}

// parseTxTimestamp decodes one message of the error queue: the transmit time
// from SCM_TIMESTAMPING (the card's when it took one, else the software one)
// and the OPT_ID packet identifier from
// the sock_extended_err of IP_RECVERR / IPV6_RECVERR (origin
// SO_EE_ORIGIN_TIMESTAMPING, info SCM_TSTAMP_SND). It reports false for any
// other message.
//
//declscope:package // ReadTxTimestamps decodes the error queue through it
func parseTxTimestamp(oob []byte) (id uint32, at time.Time, ok bool) {
	var haveID, haveAt bool
	for {
		level, typ, data, rest, more := nextCmsg(oob)
		if !more {
			return id, at, haveID && haveAt
		}
		oob = rest
		switch {
		case isTimestamping(level, typ):
			if at, haveAt = hardwareTimespec(data); !haveAt {
				at, haveAt = timespec(data, len(data)/3)
			}
		case level == unix.IPPROTO_IP && typ == unix.IP_RECVERR,
			level == unix.IPPROTO_IPV6 && typ == unix.IPV6_RECVERR:
			// struct sock_extended_err: ee_errno(4) ee_origin(1) ee_type(1)
			// ee_code(1) ee_pad(1) ee_info(4) ee_data(4).
			if len(data) < sockExtendedErrSize ||
				binary.NativeEndian.Uint32(data) != uint32(unix.ENOMSG) ||
				data[4] != unix.SO_EE_ORIGIN_TIMESTAMPING ||
				binary.NativeEndian.Uint32(data[8:]) != unix.SCM_TSTAMP_SND {
				return 0, time.Time{}, false
			}
			id, haveID = binary.NativeEndian.Uint32(data[12:]), true
		}
	}
}

// cmsgOctet decodes a payload of size octets (1, or 4 for a native int)
// holding a value in 0-255.
func cmsgOctet(data []byte, size int) (int, bool) {
	if len(data) < size {
		return 0, false
	}
	if size == 1 {
		return int(data[0]), true
	}
	v := binary.NativeEndian.Uint32(data)
	if v > 255 {
		return 0, false
	}
	return int(v), true
}
