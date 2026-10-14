//go:build linux

package tstamp

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// appendCmsg appends one control message with the given data, padded to
// CMSG_ALIGN, as the kernel lays it out.
func appendCmsg(b []byte, level, typ uint32, data []byte) []byte {
	m := make([]byte, unix.CmsgSpace(len(data)))
	putCmsgHeader(m, uint32(unix.CmsgLen(len(data))), level, typ)
	copy(m[cmsgHdrLen:], data)
	return append(b, m...)
}

func nativeInt(v int32) []byte {
	return binary.NativeEndian.AppendUint32(nil, uint32(v))
}

func TestTOSCmsg(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		b := newTOSCmsg(v6)
		putTOSCmsg(b, 0xb8)
		// The send-side layout parses back like a received message.
		meta := RxMeta{TTL: -1, TOS: -1}
		parseRxCmsgs(b, &meta)
		if v6 && meta.TOS != 0xb8 {
			t.Errorf("IPV6_TCLASS cmsg parses to TOS %#x, want 0xb8", meta.TOS)
		}
		if got := cmsgLen(b); got != uint64(unix.CmsgLen(4)) {
			t.Errorf("v6=%v: cmsg_len = %d, want %d", v6, got, unix.CmsgLen(4))
		}
		if len(b) != unix.CmsgSpace(4) {
			t.Errorf("v6=%v: len = %d, want CMSG_SPACE(4) = %d", v6, len(b), unix.CmsgSpace(4))
		}
	}
}

func TestParseRxCmsgs(t *testing.T) {
	var v4 []byte
	v4 = appendCmsg(v4, unix.IPPROTO_IP, unix.IP_TTL, nativeInt(61))
	v4 = appendCmsg(v4, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, make([]byte, 16)) // ignored
	v4 = appendCmsg(v4, unix.IPPROTO_IP, unix.IP_TOS, []byte{0xb9})

	var v6 []byte
	v6 = appendCmsg(v6, unix.IPPROTO_IPV6, unix.IPV6_TCLASS, nativeInt(0x2e))
	v6 = appendCmsg(v6, unix.IPPROTO_IPV6, unix.IPV6_HOPLIMIT, nativeInt(64))

	cases := []struct {
		name     string
		oob      []byte
		ttl, tos int
	}{
		{"ipv4", v4, 61, 0xb9},
		{"ipv6", v6, 64, 0x2e},
		{"empty", nil, -1, -1},
		{"truncated header", v4[:cmsgHdrLen-1], -1, -1},
		{"truncated second message", v4[:unix.CmsgSpace(4)+cmsgHdrLen], 61, -1},
		{"short TTL data", appendCmsg(nil, unix.IPPROTO_IP, unix.IP_TTL, []byte{1}), -1, -1},
		{"TTL out of range", appendCmsg(nil, unix.IPPROTO_IP, unix.IP_TTL, nativeInt(256)), -1, -1},
	}
	for _, tc := range cases {
		meta := RxMeta{TTL: -1, TOS: -1}
		parseRxCmsgs(tc.oob, &meta)
		if meta.TTL != tc.ttl || meta.TOS != tc.tos {
			t.Errorf("%s: TTL, TOS = %d, %#x; want %d, %#x", tc.name, meta.TTL, meta.TOS, tc.ttl, tc.tos)
		}
	}
}

func TestParseRxCmsgsBadLength(t *testing.T) {
	b := appendCmsg(nil, unix.IPPROTO_IP, unix.IP_TTL, nativeInt(61))
	for _, n := range []uint32{0, cmsgHdrLen - 1, uint32(len(b)) + 1} {
		m := append([]byte(nil), b...)
		putCmsgHeader(m, n, unix.IPPROTO_IP, unix.IP_TTL)
		meta := RxMeta{TTL: -1, TOS: -1}
		parseRxCmsgs(m, &meta)
		if meta.TTL != -1 {
			t.Errorf("cmsg_len %d: TTL = %d, want -1", n, meta.TTL)
		}
	}
}

func FuzzParseRxCmsgs(f *testing.F) {
	f.Add(appendCmsg(nil, unix.IPPROTO_IP, unix.IP_TTL, nativeInt(61)))
	f.Add(appendCmsg(nil, unix.IPPROTO_IPV6, unix.IPV6_TCLASS, nativeInt(4)))
	f.Add([]byte{})
	f.Fuzz(func(_ *testing.T, oob []byte) {
		meta := RxMeta{TTL: -1, TOS: -1}
		parseRxCmsgs(oob, &meta)
	})
}

// timespecs encodes struct timespec values with fields of width octets.
func timespecs(width int, ts ...[2]int64) []byte {
	var b []byte
	for _, t := range ts {
		for _, v := range t {
			if width == 8 {
				b = binary.NativeEndian.AppendUint64(b, uint64(v))
			} else {
				b = binary.NativeEndian.AppendUint32(b, uint32(v))
			}
		}
	}
	return b
}

func TestParseRxTimestamps(t *testing.T) {
	want := time.Unix(1790985600, 123456789)
	ts, z := [2]int64{1790985600, 123456789}, [2]int64{}
	cases := []struct {
		name string
		oob  []byte
		ok   bool
	}{
		{"SCM_TIMESTAMPING", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, timespecs(8, ts, z, z)), true},
		{"SCM_TIMESTAMPING_NEW", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING_NEW, timespecs(8, ts, z, z)), true},
		{"32-bit SCM_TIMESTAMPING", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, timespecs(4, ts, z, z)), true},
		{"SCM_TIMESTAMPNS", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, timespecs(8, ts)), true},
		{"32-bit SCM_TIMESTAMPNS_NEW", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS_NEW, timespecs(4, ts)), true},
		{"nanoseconds out of range", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, timespecs(8, [2]int64{1, 1e9})), false},
		{"odd size", appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, make([]byte, 12)), false},
	}
	for _, tc := range cases {
		meta := RxMeta{TTL: -1, TOS: -1}
		parseRxCmsgs(appendCmsg(tc.oob, unix.IPPROTO_IP, unix.IP_TTL, nativeInt(9)), &meta)
		if meta.Kernel != tc.ok || tc.ok && !meta.At.Equal(want) || meta.TTL != 9 {
			t.Errorf("%s: meta %+v", tc.name, meta)
		}
	}
}

// extendedErr encodes a struct sock_extended_err.
func extendedErr(errno uint32, origin uint8, info, data uint32) []byte {
	b := binary.NativeEndian.AppendUint32(nil, errno)
	b = append(b, origin, 0, 0, 0)
	b = binary.NativeEndian.AppendUint32(b, info)
	return binary.NativeEndian.AppendUint32(b, data)
}

func TestParseTxTimestamp(t *testing.T) {
	z := [2]int64{}
	ts := appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, timespecs(8, [2]int64{1790985600, 5}, z, z))
	stamp := func(level, typ uint32, e []byte) []byte { return appendCmsg(append([]byte(nil), ts...), level, typ, e) }
	good := extendedErr(uint32(unix.ENOMSG), unix.SO_EE_ORIGIN_TIMESTAMPING, unix.SCM_TSTAMP_SND, 42)
	cases := []struct {
		name string
		oob  []byte
		ok   bool
	}{
		{"IPv4", stamp(unix.IPPROTO_IP, unix.IP_RECVERR, good), true},
		{"IPv6", stamp(unix.IPPROTO_IPV6, unix.IPV6_RECVERR, append(good, make([]byte, 28)...)), true},
		{"error first", appendCmsg(appendCmsg(nil, unix.IPPROTO_IP, unix.IP_RECVERR, good), unix.SOL_SOCKET, unix.SO_TIMESTAMPING,
			timespecs(8, [2]int64{1790985600, 5}, z, z)), true},
		{"no error", ts, false},
		{"no timestamp", appendCmsg(nil, unix.IPPROTO_IP, unix.IP_RECVERR, good), false},
		{"ICMP error", stamp(unix.IPPROTO_IP, unix.IP_RECVERR, extendedErr(uint32(unix.ECONNREFUSED), 2, 0, 0)), false},
		{"scheduler timestamp", stamp(unix.IPPROTO_IP, unix.IP_RECVERR,
			extendedErr(uint32(unix.ENOMSG), unix.SO_EE_ORIGIN_TIMESTAMPING, unix.SCM_TSTAMP_SCHED, 42)), false},
		{"short error", stamp(unix.IPPROTO_IP, unix.IP_RECVERR, good[:12]), false},
	}
	for _, tc := range cases {
		id, at, ok := parseTxTimestamp(tc.oob)
		if ok != tc.ok || ok && (id != 42 || !at.Equal(time.Unix(1790985600, 5))) {
			t.Errorf("%s: %d %v %v", tc.name, id, at, ok)
		}
	}
}

func FuzzParseTxTimestamp(f *testing.F) {
	f.Add(appendCmsg(appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, make([]byte, 48)), unix.IPPROTO_IP, unix.IP_RECVERR,
		extendedErr(uint32(unix.ENOMSG), unix.SO_EE_ORIGIN_TIMESTAMPING, 0, 1)))
	f.Add([]byte{})
	f.Fuzz(func(_ *testing.T, oob []byte) {
		_, _, _ = parseTxTimestamp(oob)
		meta := RxMeta{}
		parseRxCmsgs(oob, &meta)
	})
}

func TestParsePktinfo(t *testing.T) {
	v4 := make([]byte, in4PktinfoSize)
	binary.NativeEndian.PutUint32(v4, 3)
	copy(v4[8:], []byte{192, 0, 2, 7})
	v6 := make([]byte, in6PktinfoSize)
	copy(v6, netip.MustParseAddr("2001:db8::7").AsSlice())
	binary.NativeEndian.PutUint32(v6[16:], 4)
	cases := []struct {
		oob []byte
		dst netip.Addr
		idx int
	}{
		{appendCmsg(nil, unix.IPPROTO_IP, unix.IP_PKTINFO, v4), netip.MustParseAddr("192.0.2.7"), 3},
		{appendCmsg(nil, unix.IPPROTO_IPV6, unix.IPV6_PKTINFO, v6), netip.MustParseAddr("2001:db8::7"), 4},
		{appendCmsg(nil, unix.IPPROTO_IP, unix.IP_PKTINFO, v4[:8]), netip.Addr{}, 0},
	}
	for _, tc := range cases {
		var meta RxMeta
		parseRxCmsgs(tc.oob, &meta)
		if meta.Dst != tc.dst || meta.IfIndex != tc.idx {
			t.Errorf("Dst %v IfIndex %d, want %v %d", meta.Dst, meta.IfIndex, tc.dst, tc.idx)
		}
	}
}

func TestPickRxTimestamp(t *testing.T) {
	base := time.Unix(1790985600, 0)
	swTS := [2]int64{1790985600, 0}
	z := [2]int64{}
	at := func(d time.Duration) [2]int64 {
		t := base.Add(d)
		return [2]int64{t.Unix(), int64(t.Nanosecond())}
	}
	cases := []struct {
		name     string
		ts       [3][2]int64
		now      time.Time
		want     time.Time
		kernel   bool
		hardware bool
		rejected bool
	}{
		{"hardware close to software", [3][2]int64{swTS, z, at(-20 * time.Microsecond)}, time.Time{}, base.Add(-20 * time.Microsecond), true, true, false},
		{"hardware at the tolerance", [3][2]int64{swTS, z, at(time.Millisecond)}, time.Time{}, base.Add(time.Millisecond), true, true, false},
		{"hardware on TAI", [3][2]int64{swTS, z, at(37 * time.Second)}, time.Time{}, base, true, false, true},
		{"hardware far behind", [3][2]int64{swTS, z, at(-2 * time.Millisecond)}, time.Time{}, base, true, false, true},
		{"software only", [3][2]int64{swTS, z, z}, time.Time{}, base, true, false, false},
		{"hardware only", [3][2]int64{z, z, at(-time.Second)}, base, base.Add(-time.Second), true, true, false},
		{"hardware only, in the future", [3][2]int64{z, z, at(time.Second)}, base, base, false, false, true},
		{"hardware only, no user time", [3][2]int64{z, z, at(time.Second)}, time.Time{}, base.Add(time.Second), true, true, false},
		{"none", [3][2]int64{z, z, z}, base, base, false, false, false},
	}
	for _, width := range []int{8, 4} {
		for _, tc := range cases {
			meta := RxMeta{At: tc.now, TTL: -1, TOS: -1}
			parseRxCmsgs(appendCmsg(nil, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, timespecs(width, tc.ts[0], tc.ts[1], tc.ts[2])), &meta)
			if !meta.At.Equal(tc.want) || meta.Kernel != tc.kernel || meta.Hardware != tc.hardware || meta.HardwareRejected != tc.rejected {
				t.Errorf("%s (%d-octet fields): meta %+v", tc.name, width, meta)
			}
		}
	}
}

func TestParseHardwareTxTimestamp(t *testing.T) {
	sw, hw, z := [2]int64{1790985600, 5}, [2]int64{1790985600, 7}, [2]int64{}
	errOK := appendCmsg(nil, unix.IPPROTO_IP, unix.IP_RECVERR, extendedErr(uint32(unix.ENOMSG), unix.SO_EE_ORIGIN_TIMESTAMPING, unix.SCM_TSTAMP_SND, 3))
	cases := []struct {
		name string
		ts   [3][2]int64
		want time.Time
	}{
		{"hardware", [3][2]int64{z, z, hw}, time.Unix(1790985600, 7)},
		{"software", [3][2]int64{sw, z, z}, time.Unix(1790985600, 5)},
		{"both prefers hardware", [3][2]int64{sw, z, hw}, time.Unix(1790985600, 7)},
	}
	for _, tc := range cases {
		oob := appendCmsg(errOK, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, timespecs(8, tc.ts[0], tc.ts[1], tc.ts[2]))
		id, at, ok := parseTxTimestamp(oob)
		if !ok || id != 3 || !at.Equal(tc.want) {
			t.Errorf("%s: %d %v %v", tc.name, id, at, ok)
		}
	}
}
