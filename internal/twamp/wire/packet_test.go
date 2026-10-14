package wire_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestSenderPacketRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8)) //nolint:gosec // reproducible test inputs
	for _, padLen := range []int{0, 1, 27, 30, 1458, 8986} {
		pad := make([]byte, padLen)
		for i := range pad {
			pad[i] = byte(r.Uint32() & 0xff)
		}
		in := wire.SenderPacket{
			Seq:           r.Uint32(),
			Timestamp:     ntptime.Timestamp(r.Uint64()),
			ErrorEstimate: errest.Estimate(r.Uint32() & 0xffff),
			Padding:       pad,
		}
		b := in.Append(nil)
		if len(b) != wire.SenderHeaderSize+padLen {
			t.Fatalf("len = %d, want %d", len(b), wire.SenderHeaderSize+padLen)
		}
		var out wire.SenderPacket
		if err := out.Decode(b); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, in) {
			t.Fatalf("round trip with %d octets of padding: got %+v", padLen, out)
		}
		if padLen > 0 && &out.Padding[0] != &b[wire.SenderHeaderSize] {
			t.Error("Decode copied the padding instead of aliasing it")
		}
	}
}

func TestReflectorPacketRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10)) //nolint:gosec // reproducible test inputs
	for _, dscpecn := range []bool{false, true} {
		for range 1000 {
			in := wire.ReflectorPacket{
				Seq:                 r.Uint32(),
				Timestamp:           ntptime.Timestamp(r.Uint64()),
				ErrorEstimate:       errest.Estimate(r.Uint32() & 0xffff),
				ReceiveTimestamp:    ntptime.Timestamp(r.Uint64()),
				SenderSeq:           r.Uint32(),
				SenderTimestamp:     ntptime.Timestamp(r.Uint64()),
				SenderErrorEstimate: errest.Estimate(r.Uint32() & 0xffff),
				SenderTTL:           uint8(r.Uint32() & 0xff),
				HasDSCPECN:          dscpecn,
				Padding:             make([]byte, r.IntN(64)),
			}
			if dscpecn {
				in.DSCPECN = wire.DSCPECN(r.Uint32() & 0xff)
			}
			b := in.Append(nil)
			out := wire.ReflectorPacket{HasDSCPECN: dscpecn}
			if err := out.Decode(b); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out, in) {
				t.Fatalf("round trip: got %+v, want %+v", out, in)
			}
		}
	}
}

func TestSenderPutHeader(t *testing.T) {
	buf := bytes.Repeat([]byte{0xcc}, 41)
	p := wire.SenderPacket{Seq: 1, Timestamp: ntptime.New(2, 3), ErrorEstimate: 0x8001, Padding: []byte{9, 9}}
	if err := p.PutHeader(buf); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0x80, 1}, bytes.Repeat([]byte{0xcc}, 27)...)
	if !bytes.Equal(buf, want) {
		t.Errorf("PutHeader wrote %x, want %x", buf, want)
	}
	if err := wire.PutTimestamp(buf, ntptime.New(0xaabbccdd, 0x11223344)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[4:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0x11, 0x22, 0x33, 0x44}) || buf[3] != 1 || buf[12] != 0x80 {
		t.Errorf("PutTimestamp wrote %x", buf[:14])
	}
	if err := p.PutHeader(buf[:13]); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("PutHeader(13 octets) = %v", err)
	}
	if err := wire.PutTimestamp(buf[:13], 0); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("PutTimestamp(13 octets) = %v", err)
	}
}

func TestDSCPECN(t *testing.T) {
	v := wire.NewDSCPECN(46, 2)
	if v != 0xba || v.DSCP() != 46 || v.ECN() != 2 {
		t.Errorf("NewDSCPECN(46, 2) = %#x (%d, %d)", uint8(v), v.DSCP(), v.ECN())
	}
	if v := wire.NewDSCPECN(63, 0xff); v != 0xff || v.DSCP() != 63 || v.ECN() != 3 {
		t.Errorf("NewDSCPECN(63, 0xff) = %#x", uint8(v))
	}
}

// senderBytes builds a Session-Sender packet of n octets whose padding counts
// up from 0.
func senderBytes(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0, 0, 0, 9, 1, 2, 3, 4, 5, 6, 7, 8, 0x80, 0x01})
	for i := wire.SenderHeaderSize; i < n; i++ {
		b[i] = byte((i - wire.SenderHeaderSize) & 0xff)
	}
	return b
}

func TestReflectLengths(t *testing.T) {
	tests := []struct {
		name      string
		n         int
		dscpecn   bool
		wantLen   int
		wantStart int // padding offset in the reply
	}{
		{"minimal sender packet", 14, false, 41, 41},
		{"padding shorter than truncation", 30, false, 41, 41},
		{"symmetric default", 41, false, 41, 41},
		{"one more", 42, false, 42, 41},
		{"STAMP base packet", 44, false, 44, 41},
		{"padding 100", 114, false, 114, 41},
		{"IPv4 MTU", 1472, false, 1472, 41},
		{"max packet size", 9000, false, 9000, 41},
		{"RFC 7750 minimal", 14, true, 44, 44},
		{"RFC 7750 symmetric", 44, true, 44, 44},
		{"RFC 7750 padding 100", 114, true, 114, 44},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := bytes.Repeat([]byte{0xee}, 9100) // stale octets from an earlier packet
			copy(buf, senderBytes(tt.n))
			p := wire.ReflectorPacket{
				Seq:              5,
				Timestamp:        ntptime.New(7, 7),
				ErrorEstimate:    errest.Unknown,
				ReceiveTimestamp: ntptime.New(6, 6),
				SenderTTL:        250,
				HasDSCPECN:       tt.dscpecn,
				DSCPECN:          wire.NewDSCPECN(10, 1),
			}
			m, err := p.Reflect(buf, tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if m != tt.wantLen {
				t.Fatalf("reply length = %d, want %d", m, tt.wantLen)
			}
			reply := buf[:m]
			var got wire.ReflectorPacket
			got.HasDSCPECN = tt.dscpecn
			if err := got.Decode(reply); err != nil {
				t.Fatal(err)
			}
			want := wire.ReflectorPacket{
				Seq:                 5,
				Timestamp:           ntptime.New(7, 7),
				ErrorEstimate:       errest.Unknown,
				ReceiveTimestamp:    ntptime.New(6, 6),
				SenderSeq:           9,
				SenderTimestamp:     ntptime.New(0x01020304, 0x05060708),
				SenderErrorEstimate: 0x8001,
				SenderTTL:           250,
				HasDSCPECN:          tt.dscpecn,
				Padding:             senderBytes(tt.n)[wire.SenderHeaderSize:][:m-tt.wantStart],
			}
			if tt.dscpecn {
				want.DSCPECN = wire.NewDSCPECN(10, 1)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("reply decodes to\n %+v\nwant\n %+v", got, want)
			}
			if !reflect.DeepEqual(p.Padding, want.Padding) || p.SenderSeq != 9 {
				t.Errorf("Reflect left p = %+v", p)
			}
			// MBZ octets are cleared even though the buffer held stale data.
			for _, i := range []int{14, 15, 38, 39} {
				if reply[i] != 0 {
					t.Errorf("MBZ octet %d = %#x", i, reply[i])
				}
			}
			if tt.dscpecn && (reply[42] != 0 || reply[43] != 0) {
				t.Errorf("RFC 7750 MBZ = %x", reply[42:44])
			}
		})
	}
}

func TestReflectShortBuffers(t *testing.T) {
	var p wire.ReflectorPacket
	tests := []struct {
		name   string
		bufLen int
		n      int
	}{
		{"packet below sender header", 100, 13},
		{"empty", 100, 0},
		{"negative", 100, -1},
		{"n beyond buffer", 20, 21},
		{"buffer cannot hold reply header", 40, 14},
	}
	for _, tt := range tests {
		if _, err := p.Reflect(make([]byte, tt.bufLen), tt.n); !errors.Is(err, wire.ErrShortBuffer) {
			t.Errorf("%s: err = %v, want ErrShortBuffer", tt.name, err)
		}
	}
	p.HasDSCPECN = true
	if _, err := p.Reflect(make([]byte, 43), 14); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("RFC 7750 reply in 43 octets: err = %v", err)
	}
	if err := p.Decode(make([]byte, 43)); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("RFC 7750 Decode of 43 octets: err = %v", err)
	}
}

func TestHotPathDoesNotAllocate(t *testing.T) {
	pkt := senderBytes(41)
	buf := make([]byte, 0, 2048)
	sp := wire.SenderPacket{Seq: 1, Padding: pkt[wire.SenderHeaderSize:]}
	var rp wire.ReflectorPacket
	rbuf := make([]byte, 2048)

	allocs := testing.AllocsPerRun(1000, func() {
		_ = sp.Append(buf[:0])
		_ = sp.Decode(pkt)
		_ = sp.PutHeader(rbuf)
		_ = wire.PutTimestamp(rbuf, 1)
		copy(rbuf, pkt)
		n, _ := rp.Reflect(rbuf, len(pkt))
		_ = rp.Decode(rbuf[:n])
		_ = rp.Append(buf[:0])
		_ = rp.Decode(pkt[:3]) // error path
		_ = sp.Decode(pkt[:3])
	})
	if allocs != 0 {
		t.Errorf("test packet path allocates %v times per run", allocs)
	}
}

func BenchmarkReflect(b *testing.B) {
	pkt := senderBytes(41)
	buf := make([]byte, 2048)
	p := wire.ReflectorPacket{Seq: 1, SenderTTL: 255}
	b.ReportAllocs()
	for b.Loop() {
		copy(buf, pkt)
		n, _ := p.Reflect(buf, len(pkt))
		_ = wire.PutTimestamp(buf[:n], 1)
	}
}

func BenchmarkSenderAppend(b *testing.B) {
	p := wire.SenderPacket{Padding: make([]byte, 27)}
	buf := make([]byte, 0, 2048)
	b.ReportAllocs()
	for b.Loop() {
		p.Seq++
		buf = p.Append(buf[:0])
	}
}

// TestSIDText checks that a SID marshals as its hexadecimal String, which
// is how log handlers and JSON encoders print it.
func TestSIDText(t *testing.T) {
	sid := wire.SID{1, 2, 0xff}
	b, err := json.Marshal(map[string]wire.SID{"sid": sid})
	if err != nil || string(b) != `{"sid":"0102ff00000000000000000000000000"}` || sid.String() != "0102ff00000000000000000000000000" {
		t.Errorf("json %s, %v; String %s", b, err, sid)
	}
	var back map[string]wire.SID
	if err := json.Unmarshal(b, &back); err != nil || back["sid"] != sid {
		t.Errorf("round trip %v, %v", back, err)
	}
	var bad wire.SID
	for _, text := range []string{"0102", "zz02ff00000000000000000000000000", ""} {
		if bad.UnmarshalText([]byte(text)) == nil {
			t.Errorf("UnmarshalText(%q) accepted", text)
		}
	}
}
