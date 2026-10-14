package wire_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// codec is what every message and packet type implements.
type codec interface {
	Append(dst []byte) []byte
	Decode(b []byte) error
}

// readGolden loads test/golden/<name>: hexadecimal octets, whitespace and
// "#" comments ignored.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "golden", name)) //nolint:gosec // names are constants of this test
	if err != nil {
		t.Fatal(err)
	}
	var digits strings.Builder
	for line := range strings.Lines(string(raw)) {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		digits.WriteString(strings.Join(strings.Fields(line), ""))
	}
	b, err := hex.DecodeString(digits.String())
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b
}

func ip4(a, b, c, d byte) [16]byte { return [16]byte{a, b, c, d} }

var (
	// 2026-10-04T00:00:00.5Z, the timestamp the golden files use.
	goldenTime = ntptime.New(0xee6c1580, 0x80000000)
	goldenEE   = errest.New(true, false, 10, 210)
	seq16      = [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
)

func seqBytes(n int, start byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func goldenCases() []struct {
	file  string
	want  codec
	fresh func() codec
} {
	var keyID [80]byte
	copy(keyID[:], "gotwamp")
	var token [64]byte
	copy(token[:], seqBytes(64, 0x20))
	return []struct {
		file  string
		want  codec
		fresh func() codec
	}{
		{"server-greeting.hex", &wire.ServerGreeting{
			Modes:     wire.ModeUnauthenticated | wire.ModeIndividualSessionControl | wire.ModeReflectOctets | wire.ModeSymmetricalSize,
			Challenge: seq16,
			Salt:      [16]byte(seqBytes(16, 0x10)),
			Count:     wire.GreetingCountDefault,
		}, func() codec { return new(wire.ServerGreeting) }},
		{"set-up-response.hex", &wire.SetUpResponse{
			Mode:     wire.ModeAuthenticated,
			KeyID:    keyID,
			Token:    token,
			ClientIV: [16]byte(seqBytes(16, 0x60)),
		}, func() codec { return new(wire.SetUpResponse) }},
		{"server-start.hex", &wire.ServerStart{
			Accept:    wire.AcceptOK,
			ServerIV:  [16]byte(seqBytes(16, 0xa0)),
			StartTime: goldenTime,
		}, func() codec { return new(wire.ServerStart) }},
		{"request-tw-session-ipv4.hex", &wire.RequestTWSession{
			IPVN:            wire.IPVN4,
			SenderPort:      49152,
			ReceiverPort:    49152,
			SenderAddress:   ip4(192, 0, 2, 1),
			ReceiverAddress: ip4(198, 51, 100, 2),
			PaddingLength:   wire.DefaultPaddingLength,
			Timeout:         ntptime.New(2, 0),
			TypeP:           wire.TypePFromDSCP(46),
		}, func() codec { return new(wire.RequestTWSession) }},
		{"request-tw-session-ipv6.hex", &wire.RequestTWSession{
			IPVN:             wire.IPVN6,
			SenderPort:       54689,
			ReceiverPort:     862,
			SenderAddress:    [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 1},
			ReceiverAddress:  [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 2},
			PaddingLength:    1438,
			StartTime:        goldenTime,
			Timeout:          ntptime.New(1, 1<<31),
			OctetsToReflect:  4,
			PaddingToReflect: 16,
		}, func() codec { return new(wire.RequestTWSession) }},
		{"accept-session.hex", &wire.AcceptSession{
			Accept:          wire.AcceptOK,
			Port:            20000,
			SID:             wire.SID{192, 0, 2, 1, 0xee, 0x6c, 0x15, 0x80, 0x80, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef},
			ReflectedOctets: 4,
			ServerOctets:    0xabcd,
		}, func() codec { return new(wire.AcceptSession) }},
		{"start-sessions.hex", &wire.StartSessions{}, func() codec { return new(wire.StartSessions) }},
		{"start-ack.hex", &wire.StartAck{Accept: wire.AcceptTemporaryLimit}, func() codec { return new(wire.StartAck) }},
		{"stop-sessions.hex", &wire.StopSessions{Accept: wire.AcceptOK, Sessions: 1}, func() codec { return new(wire.StopSessions) }},
		{"sender-packet.hex", &wire.SenderPacket{
			Seq:           7,
			Timestamp:     goldenTime,
			ErrorEstimate: goldenEE,
			Padding:       append(seqBytes(16, 0xf0), seqBytes(11, 0xe0)...),
		}, func() codec { return new(wire.SenderPacket) }},
		{"reflector-packet.hex", &wire.ReflectorPacket{
			Seq:                 3,
			Timestamp:           ntptime.New(0xee6c1580, 0x80010000),
			ErrorEstimate:       errest.New(false, false, 0, 1),
			ReceiveTimestamp:    ntptime.New(0xee6c1580, 0x80008000),
			SenderSeq:           7,
			SenderTimestamp:     goldenTime,
			SenderErrorEstimate: goldenEE,
			SenderTTL:           255,
			Padding:             []byte{},
		}, func() codec { return new(wire.ReflectorPacket) }},
		{"reflector-packet-padding.hex", &wire.ReflectorPacket{
			ErrorEstimate:       errest.Unknown,
			ReceiveTimestamp:    ntptime.New(2208988801, 1<<30),
			SenderSeq:           0xffffffff,
			SenderTimestamp:     ntptime.New(2208988801, 0),
			SenderErrorEstimate: errest.New(true, false, 0, 1),
			SenderTTL:           64,
			Padding:             seqBytes(73, 0),
		}, func() codec { return new(wire.ReflectorPacket) }},
		{"reflector-packet-dscpecn.hex", &wire.ReflectorPacket{
			Seq:                 42,
			Timestamp:           ntptime.New(0xee6c1580, 0x80010000),
			ErrorEstimate:       errest.New(true, true, 0, 5),
			ReceiveTimestamp:    ntptime.New(0xee6c1580, 0x80008000),
			SenderSeq:           42,
			SenderTimestamp:     goldenTime,
			SenderErrorEstimate: goldenEE,
			SenderTTL:           254,
			HasDSCPECN:          true,
			DSCPECN:             wire.NewDSCPECN(46, 2),
			Padding:             []byte{0xde, 0xad, 0xbe, 0xef},
		}, func() codec { return &wire.ReflectorPacket{HasDSCPECN: true} }},
	}
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.file, func(t *testing.T) {
			golden := readGolden(t, tc.file)

			if enc := tc.want.Append(nil); !bytes.Equal(enc, golden) {
				t.Errorf("Append mismatch\n got  %x\n want %x", enc, golden)
			}
			// Appending keeps what dst already holds.
			prefix := []byte{0xaa, 0xbb}
			if enc := tc.want.Append(prefix); !bytes.Equal(enc[:2], prefix) || !bytes.Equal(enc[2:], golden) {
				t.Errorf("Append(prefix) = %x", enc)
			}

			got := tc.fresh()
			if err := got.Decode(golden); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode mismatch\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

// TestGoldenReflect turns the golden Session-Sender packet into the golden
// Session-Reflector packet with Reflect, as a reflector does in place.
func TestGoldenReflect(t *testing.T) {
	sender := readGolden(t, "sender-packet.hex")
	want := readGolden(t, "reflector-packet.hex")

	buf := make([]byte, 2048)
	n := copy(buf, sender)
	p := wire.ReflectorPacket{
		Seq:              3,
		ErrorEstimate:    errest.New(false, false, 0, 1),
		ReceiveTimestamp: ntptime.New(0xee6c1580, 0x80008000),
		SenderTTL:        255,
	}
	m, err := p.Reflect(buf, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.PutTimestamp(buf[:m], ntptime.New(0xee6c1580, 0x80010000)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf[:m], want) {
		t.Errorf("reflected\n got  %x\n want %x", buf[:m], want)
	}
	if p.SenderSeq != 7 || p.SenderTimestamp != goldenTime || p.SenderErrorEstimate != goldenEE {
		t.Errorf("Reflect did not record the sender fields: %+v", p)
	}
}

// ignoredOctets lists, per golden file, the octets a receiver must ignore:
// MBZ, Unused, HMAC (unauthenticated mode), the high nibble of the IPVN
// octet and the trailing 12 octets of IPv4 addresses.
var ignoredOctets = map[string][]struct {
	from, to int
	mask     byte
}{
	"server-greeting.hex":          {{0, 12, 0xff}, {52, 64, 0xff}},
	"server-start.hex":             {{0, 15, 0xff}, {40, 48, 0xff}},
	"request-tw-session-ipv4.hex":  {{1, 2, 0xf0}, {20, 32, 0xff}, {36, 48, 0xff}, {92, 112, 0xff}},
	"request-tw-session-ipv6.hex":  {{1, 2, 0xf0}, {92, 112, 0xff}},
	"accept-session.hex":           {{1, 2, 0xff}, {24, 48, 0xff}},
	"start-sessions.hex":           {{1, 32, 0xff}},
	"start-ack.hex":                {{1, 32, 0xff}},
	"stop-sessions.hex":            {{2, 4, 0xff}, {8, 32, 0xff}},
	"reflector-packet.hex":         {{14, 16, 0xff}, {38, 40, 0xff}},
	"reflector-packet-padding.hex": {{14, 16, 0xff}, {38, 40, 0xff}},
	"reflector-packet-dscpecn.hex": {{14, 16, 0xff}, {38, 40, 0xff}, {42, 44, 0xff}},
}

// TestGoldenIgnoresMBZ sets every ignored octet of each golden message and
// checks that decoding is unchanged and that re-encoding clears them again.
func TestGoldenIgnoresMBZ(t *testing.T) {
	for _, tc := range goldenCases() {
		ranges, ok := ignoredOctets[tc.file]
		if !ok {
			continue
		}
		t.Run(tc.file, func(t *testing.T) {
			golden := readGolden(t, tc.file)
			dirty := bytes.Clone(golden)
			for _, r := range ranges {
				for i := r.from; i < r.to; i++ {
					dirty[i] |= r.mask
				}
			}
			got := tc.fresh()
			if err := got.Decode(dirty); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode with MBZ set\n got  %+v\n want %+v", got, tc.want)
			}
			if enc := got.Append(nil); !bytes.Equal(enc, golden) {
				t.Errorf("re-encoding did not clear MBZ\n got  %x\n want %x", enc, golden)
			}
		})
	}
}

// TestGoldenShortBuffer feeds every golden message cut by one octet.
func TestGoldenShortBuffer(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.file, func(t *testing.T) {
			golden := readGolden(t, tc.file)
			minLen := len(golden)
			switch w := tc.want.(type) {
			case *wire.SenderPacket:
				minLen -= len(w.Padding)
			case *wire.ReflectorPacket:
				minLen -= len(w.Padding)
			}
			for _, n := range []int{0, 1, minLen - 1} {
				err := tc.fresh().Decode(golden[:n])
				if !errors.Is(err, wire.ErrShortBuffer) {
					t.Errorf("Decode(%d octets) error = %v, want ErrShortBuffer", n, err)
				}
			}
			// Trailing octets after a control message are not read.
			got := tc.fresh()
			if err := got.Decode(append(bytes.Clone(golden), 0xff)); err != nil {
				t.Fatalf("Decode with a trailing octet: %v", err)
			}
		})
	}
}
