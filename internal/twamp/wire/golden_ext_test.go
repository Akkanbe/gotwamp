//declscope:namespace golden

package wire_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// The SIDs of the RFC 5938 golden files.
var (
	goldenSID1 = wire.SID{0xc0, 0, 2, 1, 0xee, 0x6c, 0x15, 0x80, 0x80, 15: 1}
	goldenSID2 = wire.SID{0xc0, 0, 2, 1, 0xee, 0x6c, 0x15, 0x80, 0x80, 15: 2}
)

func TestGoldenSessionControl(t *testing.T) {
	for _, c := range []struct {
		file string
		msg  wire.SessionControl
	}{
		{"start-n-sessions.hex", wire.SessionControl{Command: wire.CommandStartNSessions, SIDs: []wire.SID{goldenSID1, goldenSID2}}},
		{"stop-n-ack.hex", wire.SessionControl{Command: wire.CommandStopNAck, Accept: wire.AcceptTemporaryLimit, SIDs: []wire.SID{goldenSID2}}},
	} {
		t.Run(c.file, func(t *testing.T) {
			b := readGolden(t, c.file)
			if got := c.msg.Append(nil); !bytes.Equal(got, b) {
				t.Errorf("Append\n got  %x\n want %x", got, b)
			}
			if n := wire.SessionControlSize(int(wire.SessionControlCount(b))); n != len(b) {
				t.Errorf("size from the header %d, want %d", n, len(b))
			}
			var m wire.SessionControl
			if err := m.Decode(b); err != nil || !reflect.DeepEqual(m, c.msg) {
				t.Errorf("Decode = %+v, %v", m, err)
			}
			// Accept is MBZ in the client's messages and ignored there.
			mbz := bytes.Clone(b)
			mbz[2], mbz[11] = 0xff, 0xff
			if !c.msg.Command.IsSessionControl() || m.Decode(mbz) != nil || !reflect.DeepEqual(m, c.msg) {
				t.Errorf("MBZ octets changed the decoding: %+v", m)
			}
		})
	}
	var m wire.SessionControl
	b := readGolden(t, "start-n-sessions.hex")
	if err := m.Decode(b[:len(b)-1]); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("truncated: %v", err)
	}
	if err := m.Decode(b[:15]); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("short header: %v", err)
	}
	bad := bytes.Clone(b)
	bad[0] = byte(wire.CommandStartSessions)
	if err := m.Decode(bad); !errors.Is(err, wire.ErrUnexpectedCommand) {
		t.Errorf("command 2: %v", err)
	}
	huge := bytes.Clone(b)
	huge[12] = 0xff // 4 billion SIDs: the length check must not overflow
	if err := m.Decode(huge); !errors.Is(err, wire.ErrShortBuffer) {
		t.Errorf("huge count: %v", err)
	}
}

// goldenReplyFields are the reflector fields of the reply golden files.
func goldenReplyFields() wire.ReflectorPacket {
	return wire.ReflectorPacket{
		Seq:              3,
		ErrorEstimate:    errest.New(false, false, 0, 1),
		ReceiveTimestamp: ntptime.New(0xee6c1580, 0x80008000),
		SenderTTL:        255,
	}
}

var goldenT3 = ntptime.New(0xee6c1580, 0x80010000)

func TestGoldenSymmetricalSize(t *testing.T) {
	sender := readGolden(t, "sender-packet-symmetrical.hex")
	buf := make([]byte, 2048)
	n := copy(buf, sender)
	p := goldenReplyFields()
	p.SymmetricalSize = true
	m, err := p.Reflect(buf, n)
	if err != nil {
		t.Fatal(err)
	}
	_ = wire.PutTimestamp(buf[:m], goldenT3)
	if want := readGolden(t, "reflector-packet-symmetrical.hex"); !bytes.Equal(buf[:m], want) || m != len(sender) {
		t.Errorf("reflected\n got  %x\n want %x", buf[:m], want)
	}
	// The layout finds the padding after the 41-octet sender header.
	var layout wire.PaddingLayout
	layout.SymmetricalSize, layout.ReflectLength = true, 4
	refl, extra := layout.Sender(sender[layout.SenderHeaderSize(wire.SecurityUnauthenticated):])
	if !bytes.Equal(refl, []byte{0, 1, 2, 3}) || len(extra) != 4 {
		t.Errorf("sender parts %x %x", refl, extra)
	}
}

func TestGoldenSTAMP(t *testing.T) {
	sender := readGolden(t, "stamp-sender.hex")
	b := make([]byte, len(sender))
	s := wire.STAMPSenderPacket{Seq: 7, Timestamp: goldenTime, ErrorEstimate: goldenEE, SSID: 0x1234}
	if err := s.PutHeader(b); err != nil {
		t.Fatal(err)
	}
	copy(b[wire.STAMPBaseSize+4:], []byte{0xde, 0xad, 0xbe, 0xef})
	if err := wire.PutExtraPadding(b[wire.STAMPBaseSize : wire.STAMPBaseSize+8]); err != nil {
		t.Fatal(err)
	}
	copy(b[wire.STAMPBaseSize+8:], []byte{0x80, 0x63, 0, 2, 0xab, 0xcd})
	if !bytes.Equal(b, sender) {
		t.Errorf("sender\n got  %x\n want %x", b, sender)
	}

	buf := make([]byte, 2048)
	n := copy(buf, sender)
	p := goldenReplyFields()
	p.Seq, p.SenderTTL = 0, 64
	p.HasDSCPECN, p.DSCPECN = true, 0xb8 // not part of STAMP: ignored
	m, err := p.ReflectSTAMP(buf, n)
	if err != nil {
		t.Fatal(err)
	}
	if got := wire.MarkSTAMPTLVs(p.Padding); got != 2 {
		t.Errorf("walked %d TLVs, want 2", got)
	}
	_ = wire.PutTimestamp(buf[:m], goldenT3)
	if want := readGolden(t, "stamp-reflector.hex"); !bytes.Equal(buf[:m], want) {
		t.Errorf("reflected\n got  %x\n want %x", buf[:m], want)
	}
	if p.SSID != 0x1234 || wire.STAMPSSID(buf[:m]) != 0x1234 || p.SenderSeq != 7 {
		t.Errorf("reflect recorded %+v", p)
	}
	// A TWAMP decoder reads the STAMP reply: the fields are where TWAMP has them.
	var r wire.ReflectorPacket
	if err := r.Decode(buf[:m]); err != nil || r.SenderSeq != 7 || r.SenderTTL != 64 || r.SSID != 0 {
		t.Errorf("TWAMP decoding of the STAMP reply: %+v, %v", r, err)
	}
}

func TestSTAMPShortAndTLVs(t *testing.T) {
	// A 14-octet TWAMP sender packet gets the 44-octet base, zero filled.
	buf := bytes.Repeat([]byte{0xee}, 64)
	n := copy(buf, readGolden(t, "sender-packet.hex")[:wire.SenderHeaderSize])
	var p wire.ReflectorPacket
	m, err := p.ReflectSTAMP(buf, n)
	if err != nil || m != wire.STAMPBaseSize || p.SSID != 0 || !bytes.Equal(buf[38:44], []byte{0, 0, 0, 0, 0, 0}) || buf[40] != 0 {
		t.Errorf("short request: %d, %v, %x", m, err, buf[:m])
	}
	if _, err := p.ReflectSTAMP(make([]byte, 43), 14); err == nil {
		t.Error("ReflectSTAMP into a buffer shorter than the base")
	}
	if _, err := p.ReflectSTAMP(make([]byte, 44), 13); err == nil {
		t.Error("ReflectSTAMP of a 13-octet packet")
	}

	tlvs := []byte{0x80, 1, 0, 1, 0xaa, 0x00, 2, 0, 9, 1, 2, 3}
	if n := wire.MarkSTAMPTLVs(tlvs); n != 2 || tlvs[0] != wire.TLVUnrecognized || tlvs[5] != wire.TLVMalformed {
		t.Errorf("MarkSTAMPTLVs = %d, %x", n, tlvs)
	}
	// Padding of a TWAMP Light sender is not taken for TLVs.
	for _, first := range []byte{0x00, 0xa5, 0xc0, 0xff} {
		padding := []byte{first, 1, 0, 0}
		if n := wire.MarkSTAMPTLVs(padding); n != 0 || padding[0] != first {
			t.Errorf("padding starting %#x walked as %d TLVs", first, n)
		}
	}
	if n := wire.MarkSTAMPTLVs([]byte{1, 2, 3}); n != 0 {
		t.Errorf("3 octets walked as %d TLVs", n)
	}
	if wire.PutExtraPadding(make([]byte, 3)) == nil || wire.PutExtraPadding(make([]byte, 4+0x10000)) == nil {
		t.Error("PutExtraPadding accepted a size it cannot encode")
	}
	if (&wire.STAMPSenderPacket{}).PutHeader(make([]byte, 43)) == nil || wire.STAMPSSID(make([]byte, 15)) != 0 {
		t.Error("short STAMP buffers")
	}
}
