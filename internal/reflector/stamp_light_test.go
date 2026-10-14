//declscope:namespace light

package reflector_test

import (
	"bytes"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// stampPacket is a STAMP Session-Sender packet with ssid and tlvs after the
// 44-octet base.
func stampPacket(seq uint32, ssid uint16, tlvs []byte) []byte {
	b := make([]byte, wire.STAMPBaseSize+len(tlvs))
	p := wire.STAMPSenderPacket{Seq: seq, Timestamp: 0x1122334455667788, ErrorEstimate: 0x8001, SSID: ssid}
	_ = p.PutHeader(b)
	copy(b[wire.STAMPBaseSize:], tlvs)
	return b
}

// TestLightSTAMP reflects STAMP packets in the STAMP format (RFC 8762
// §4.3.1): the SSID comes back, offsets 41–43 are MBZ, the TLVs come back
// with the U flag, and the reply is as long as the request.
func TestLightSTAMP(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{STAMP: true}, reflector.Deps{})
		tlvs := []byte{0x80, 1, 0, 4, 9, 9, 9, 9, 0x00, 0x63, 0, 1, 7} // the second without U, as a careless sender
		in := stampPacket(5, 0xabcd, tlvs)
		p, tx := h.reflect(0, in, senderA, tstamp.RxMeta{TTL: 250, TOS: 0})
		if len(tx.b) != len(in) || wire.STAMPSSID(tx.b) != 0xabcd || !bytes.Equal(tx.b[41:44], []byte{0, 0, 0}) {
			t.Errorf("reply %x", tx.b)
		}
		if p.SenderSeq != 5 || p.SenderTTL != 250 || p.SenderTimestamp != 0x1122334455667788 || p.Seq != 0 {
			t.Errorf("reply fields %+v", p)
		}
		want := bytes.Clone(tlvs)
		want[8] |= wire.TLVUnrecognized
		if !bytes.Equal(tx.b[wire.STAMPBaseSize:], want) {
			t.Errorf("tlvs %x, want %x", tx.b[wire.STAMPBaseSize:], want)
		}
		// A TWAMP packet shorter than the base gets the base packet.
		_, tx = h.reflect(0, senderPacket(6, 14), senderA, tstamp.RxMeta{TTL: 64, TOS: 0})
		if len(tx.b) != wire.STAMPBaseSize || wire.STAMPSSID(tx.b) != 0 {
			t.Errorf("reply to 14 octets: %x", tx.b)
		}
		// Padding of a TWAMP Light sender comes back untouched.
		in = stampPacket(8, 0, bytes.Repeat([]byte{0x5a}, 20))
		_, tx = h.reflect(0, in, senderA, tstamp.RxMeta{TTL: 64, TOS: 0})
		if !bytes.Equal(tx.b[wire.STAMPBaseSize:], in[wire.STAMPBaseSize:]) {
			t.Errorf("padding changed: %x", tx.b[wire.STAMPBaseSize:])
		}
		// A malformed TLV is marked and ends the walk.
		in = stampPacket(7, 1, []byte{0x80, 1, 0, 9, 1, 2})
		_, tx = h.reflect(0, in, senderA, tstamp.RxMeta{TTL: 64, TOS: 0})
		if tx.b[wire.STAMPBaseSize] != wire.TLVUnrecognized|wire.TLVMalformed {
			t.Errorf("malformed tlv flags %#x", tx.b[wire.STAMPBaseSize])
		}
	})
}

// TestLightSTAMPStateless copies the Sender Sequence Number, the STAMP
// stateless mode (RFC 8762 §4.3).
func TestLightSTAMPStateless(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{STAMP: true, Sequence: reflector.SequenceCopySender}, reflector.Deps{})
		p, _ := h.reflect(0, stampPacket(77, 3, nil), senderA, tstamp.RxMeta{TTL: 64, TOS: 0})
		if p.Seq != 77 {
			t.Errorf("reflector sequence %d, want the sender's 77", p.Seq)
		}
	})
}

func TestLightSTAMPConfig(t *testing.T) {
	err := reflector.Config{STAMP: true, DSCPECNMonitoring: true}.WithDefaults().Validate()
	if err == nil || !strings.Contains(err.Error(), "stamp and dscp_ecn_monitoring") {
		t.Errorf("err = %v", err)
	}
}
