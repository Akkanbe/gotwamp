package wire_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// fuzzSeeds adds the encoding of each message, its truncation, and a buffer
// of all-ones octets to the corpus.
func fuzzSeeds(f *testing.F, msgs ...interface{ Append([]byte) []byte }) {
	for _, m := range msgs {
		b := m.Append(nil)
		f.Add(b)
		f.Add(b[:len(b)-1])
		f.Add(bytes.Repeat([]byte{0xff}, len(b)))
	}
	f.Add([]byte{})
}

// fuzzRoundTrip decodes b into a fresh value; when that succeeds, the value
// must re-encode to octets that decode to the same value, and the encoding
// must not exceed the input (every octet the decoder reads came from b).
func fuzzRoundTrip[T any, P interface {
	*T
	Append([]byte) []byte
	Decode([]byte) error
}](t *testing.T, b []byte, init func(P)) {
	var first T
	if init != nil {
		init(&first)
	}
	if err := P(&first).Decode(b); err != nil {
		return
	}
	enc := P(&first).Append(nil)
	if len(enc) > len(b) {
		t.Fatalf("encoding of %x is %d octets, longer than the input", b, len(enc))
	}
	var second T
	if init != nil {
		init(&second)
	}
	if err := P(&second).Decode(enc); err != nil {
		t.Fatalf("re-decoding %x: %v", enc, err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round trip changed the value\n first  %+v\n second %+v", first, second)
	}
}

func FuzzDecodeServerGreeting(f *testing.F) {
	fuzzSeeds(f, &wire.ServerGreeting{Modes: 0x71, Count: 2048})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.ServerGreeting](t, b, nil) })
}

func FuzzDecodeSetUpResponse(f *testing.F) {
	fuzzSeeds(f, &wire.SetUpResponse{Mode: 1})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.SetUpResponse](t, b, nil) })
}

func FuzzDecodeServerStart(f *testing.F) {
	fuzzSeeds(f, &wire.ServerStart{StartTime: 1 << 40})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.ServerStart](t, b, nil) })
}

func FuzzDecodeRequestTWSession(f *testing.F) {
	fuzzSeeds(f,
		&wire.RequestTWSession{IPVN: 4, SenderPort: 49152, ReceiverPort: 49152, PaddingLength: 27, Timeout: 2 << 32},
		&wire.RequestTWSession{IPVN: 6, SenderAddress: [16]byte{0x20, 1, 0xd, 0xb8, 15: 1}, TypeP: 46 << 24},
	)
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.RequestTWSession](t, b, nil) })
}

func FuzzDecodeAcceptSession(f *testing.F) {
	fuzzSeeds(f, &wire.AcceptSession{Port: 20000, SID: wire.SID{1, 2, 3}})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.AcceptSession](t, b, nil) })
}

func FuzzDecodeStartSessions(f *testing.F) {
	fuzzSeeds(f, &wire.StartSessions{})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.StartSessions](t, b, nil) })
}

func FuzzDecodeStartAck(f *testing.F) {
	fuzzSeeds(f, &wire.StartAck{Accept: 5})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.StartAck](t, b, nil) })
}

func FuzzDecodeStopSessions(f *testing.F) {
	fuzzSeeds(f, &wire.StopSessions{Sessions: 1})
	f.Fuzz(func(t *testing.T, b []byte) { fuzzRoundTrip[wire.StopSessions](t, b, nil) })
}

func FuzzDecodeSenderPacket(f *testing.F) {
	fuzzSeeds(f, &wire.SenderPacket{Seq: 1, Padding: make([]byte, 27)}, &wire.SenderPacket{Padding: make([]byte, 30)})
	f.Fuzz(func(t *testing.T, b []byte) {
		fuzzRoundTrip[wire.SenderPacket](t, b, nil)
		var p wire.SenderPacket
		if p.Decode(b) == nil && !bytes.Equal(p.Append(nil), b) {
			t.Fatalf("sender packet %x does not re-encode to itself", b)
		}
	})
}

func FuzzDecodeReflectorPacket(f *testing.F) {
	fuzzSeeds(f, &wire.ReflectorPacket{SenderTTL: 255}, &wire.ReflectorPacket{HasDSCPECN: true, DSCPECN: 0xba, Padding: []byte{1}})
	f.Fuzz(func(t *testing.T, b []byte) {
		fuzzRoundTrip(t, b, func(*wire.ReflectorPacket) {})
		fuzzRoundTrip(t, b, func(p *wire.ReflectorPacket) { p.HasDSCPECN = true })
	})
}

// FuzzReflect checks that Reflect never panics or writes past b, and that a
// successful reply has the documented length and decodes back to the sender
// fields.
func FuzzReflect(f *testing.F) {
	f.Add(make([]byte, 41), 41, 2048, false)
	f.Add(make([]byte, 14), 14, 41, false)
	f.Add(make([]byte, 44), 44, 44, true)
	f.Add(bytes.Repeat([]byte{0xff}, 200), 200, 200, false)
	f.Fuzz(func(t *testing.T, pkt []byte, n, bufLen int, dscpecn bool) {
		if bufLen < 0 || bufLen > 1<<16 {
			return
		}
		buf := make([]byte, bufLen, bufLen+8)
		copy(buf, pkt)
		canary := []byte{1, 2, 3, 4, 5, 6, 7, 8}
		copy(buf[bufLen:bufLen+8], canary)
		var sender wire.SenderPacket
		senderErr := sender.Decode(bytes.Clone(buf[:max(0, min(n, bufLen))]))

		p := wire.ReflectorPacket{HasDSCPECN: dscpecn, Seq: 77, SenderTTL: 9}
		m, err := p.Reflect(buf, n)
		if !bytes.Equal(buf[bufLen:bufLen+8], canary) {
			t.Fatal("Reflect wrote past len(b)")
		}
		if err != nil {
			return
		}
		hdr := wire.ReflectorHeaderSize
		if dscpecn {
			hdr = wire.ReflectorDSCPECNHeaderSize
		}
		if m != max(n, hdr) {
			t.Fatalf("reply length %d for n=%d", m, n)
		}
		if senderErr != nil {
			t.Fatalf("Reflect accepted a packet Decode rejects: %v", senderErr)
		}
		got := wire.ReflectorPacket{HasDSCPECN: dscpecn}
		if err := got.Decode(buf[:m]); err != nil {
			t.Fatal(err)
		}
		if got.Seq != 77 || got.SenderTTL != 9 || got.SenderSeq != sender.Seq || got.SenderTimestamp != sender.Timestamp || got.SenderErrorEstimate != sender.ErrorEstimate {
			t.Fatalf("reply %+v does not match sender %+v", got, sender)
		}
		if !bytes.HasPrefix(sender.Padding, got.Padding) {
			t.Fatalf("reply padding %x is not a prefix of the sender padding %x", got.Padding, sender.Padding)
		}
	})
}

func FuzzDecodeSessionControl(f *testing.F) {
	fuzzSeeds(f,
		&wire.SessionControl{Command: wire.CommandStartNSessions, SIDs: []wire.SID{{1}, {2}}},
		&wire.SessionControl{Command: wire.CommandStopNAck, Accept: wire.AcceptFailure, SIDs: []wire.SID{{3}}},
		&wire.SessionControl{Command: wire.CommandStopNSessions},
	)
	f.Fuzz(func(t *testing.T, b []byte) {
		fuzzRoundTrip[wire.SessionControl](t, b, nil)
	})
}

// FuzzReflectSTAMP checks the STAMP reflection: it stays within its buffer,
// keeps the request length (at least the base), copies the SSID and the
// octets after the base, and the TLV walk never runs past them.
func FuzzReflectSTAMP(f *testing.F) {
	f.Add(make([]byte, 44), 44)
	f.Add(make([]byte, 14), 14)
	f.Add(append(make([]byte, 44), 0x80, 1, 0, 2, 9, 9, 0, 3, 0xff, 0xff), 54)
	f.Fuzz(func(t *testing.T, pkt []byte, n int) {
		bufLen := max(len(pkt), wire.STAMPBaseSize)
		buf := make([]byte, bufLen, bufLen+8)
		copy(buf, pkt)
		canary := []byte{1, 2, 3, 4, 5, 6, 7, 8}
		copy(buf[bufLen:bufLen+8], canary)
		orig := bytes.Clone(buf)
		var p wire.ReflectorPacket
		m, err := p.ReflectSTAMP(buf, n)
		if err != nil {
			return
		}
		if m != max(n, wire.STAMPBaseSize) {
			t.Fatalf("reply length %d for n=%d", m, n)
		}
		if n >= 16 && wire.STAMPSSID(buf[:m]) != wire.STAMPSSID(orig[:n]) {
			t.Fatal("SSID not copied")
		}
		if n > wire.STAMPBaseSize && !bytes.Equal(buf[wire.STAMPBaseSize:n], orig[wire.STAMPBaseSize:n]) {
			t.Fatal("octets after the base changed")
		}
		wire.MarkSTAMPTLVs(p.Padding)
		if !bytes.Equal(buf[bufLen:bufLen+8], canary) {
			t.Fatal("wrote past the buffer")
		}
	})
}
