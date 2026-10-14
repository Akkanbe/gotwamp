//declscope:namespace golden

package wire_test

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// authVectors loads test/golden/auth-vectors.txt, computed by
// auth-vectors.py with Python and openssl, independently of this package.
func authVectors(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "..", "test", "golden", "auth-vectors.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v := map[string][]byte{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, h, ok := strings.Cut(sc.Text(), " ")
		if !ok || strings.HasPrefix(name, "#") {
			continue
		}
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v[name] = b
	}
	return v
}

// The inputs of auth-vectors.py.
var (
	vecSalt      = [16]byte(seqBytes(16, 0x00))
	vecChallenge = [16]byte(seqBytes(16, 0x10))
	vecKeys      = wire.SessionKeys{AES: [16]byte(seqBytes(16, 0x20)), HMAC: [32]byte(seqBytes(32, 0x30))}
	vecSID       = wire.SID(seqBytes(16, 0x50))
	vecClientIV  = [16]byte(seqBytes(16, 0x60))
	vecServerIV  = [16]byte(seqBytes(16, 0x70))
)

func TestAuthVectorsKeys(t *testing.T) {
	v := authVectors(t)
	dk, err := wire.DeriveKey([]byte("secretpass"), vecSalt, 2048)
	if err != nil || !bytes.Equal(dk[:], v["dk"]) {
		t.Fatalf("DeriveKey = %x, %v; want %x", dk, err, v["dk"])
	}
	tok := wire.EncryptToken(dk, vecChallenge, vecKeys)
	if !bytes.Equal(tok[:], v["token"]) {
		t.Errorf("token = %x, want %x", tok, v["token"])
	}
	ch, k := wire.DecryptToken(dk, tok)
	if ch != vecChallenge || k != vecKeys {
		t.Errorf("DecryptToken = %x %+v", ch, k)
	}
	tk := wire.DeriveTestKeys(vecKeys, vecSID)
	if !bytes.Equal(tk.AES[:], v["testaes"]) || !bytes.Equal(tk.HMAC[:], v["testhmac"]) {
		t.Errorf("test keys = %x %x, want %x %x", tk.AES, tk.HMAC, v["testaes"], v["testhmac"])
	}
	if _, err := wire.DeriveKey([]byte("x"), vecSalt, 0); err == nil {
		t.Error("count 0 accepted")
	}
}

// vecSenderPlain is the Session-Sender plaintext of auth-vectors.py.
func vecSenderPlain() []byte {
	b := make([]byte, wire.AuthSenderHeaderSize+4)
	p := wire.AuthSenderPacket{Seq: 7, Timestamp: 0x0102030405060708, ErrorEstimate: 0x8001}
	_ = p.PutHeader(b)
	copy(b[wire.AuthSenderHeaderSize:], bytes.Repeat([]byte{0xaa}, 4))
	return b
}

func vecReflectorPlain() []byte {
	p := wire.AuthReflectorPacket{
		Seq: 3, Timestamp: 0x1112131415161718, ErrorEstimate: 0x8002, ReceiveTimestamp: 0x2122232425262728,
		SenderSeq: 7, SenderTimestamp: 0x0102030405060708, SenderErrorEstimate: 0x8001, SenderTTL: 64,
	}
	return p.Append(nil)
}

func TestAuthVectorsTestPackets(t *testing.T) {
	v := authVectors(t)
	keys := wire.DeriveTestKeys(vecKeys, vecSID)
	for _, mode := range []struct {
		name string
		m    wire.SecurityMode
	}{{"auth", wire.SecurityAuthenticated}, {"enc", wire.SecurityEncrypted}} {
		t.Run(mode.name, func(t *testing.T) {
			c, err := wire.NewTestCrypto(mode.m, keys)
			if err != nil {
				t.Fatal(err)
			}
			b := vecSenderPlain()
			if err := c.SealSender(b); err != nil || !bytes.Equal(b, v["sender_"+mode.name]) {
				t.Fatalf("SealSender = %x, %v; want %x", b, err, v["sender_"+mode.name])
			}
			if err := c.OpenSender(b); err != nil || !bytes.Equal(b[:32], vecSenderPlain()[:32]) {
				t.Errorf("OpenSender = %x, %v", b, err)
			}
			r := vecReflectorPlain()
			if err := c.SealReflector(r); err != nil || !bytes.Equal(r, v["reflector_"+mode.name]) {
				t.Fatalf("SealReflector = %x, %v; want %x", r, err, v["reflector_"+mode.name])
			}
			if err := c.OpenReflector(r); err != nil {
				t.Fatal(err)
			}
			var got wire.AuthReflectorPacket
			if err := got.Decode(r); err != nil || got.SenderSeq != 7 || got.Timestamp != 0x1112131415161718 || got.SenderTTL != 64 {
				t.Errorf("decoded %+v, %v", got, err)
			}
		})
	}
}

func TestAuthVectorsControl(t *testing.T) {
	v := authVectors(t)
	client := wire.NewControlCrypto(vecKeys, vecClientIV, vecServerIV)
	server := wire.NewControlCrypto(vecKeys, vecServerIV, vecClientIV)

	// Client → server: the first HMAC covers only Request-TW-Session 0–95.
	req := make([]byte, wire.RequestTWSessionSize)
	req[0], req[1] = 5, 4
	plain := bytes.Clone(req)
	if err := client.Seal(req, true); err != nil || !bytes.Equal(req, v["request"]) {
		t.Fatalf("request = %x, %v; want %x", req, err, v["request"])
	}
	if err := server.Open(req[:16]); err != nil {
		t.Fatal(err)
	}
	if err := server.Open(req[16:]); err != nil {
		t.Fatal(err)
	}
	if err := server.Verify(req); err != nil || !bytes.Equal(req[:96], plain[:96]) {
		t.Fatalf("server verify: %v", err)
	}

	// Server → client: the Start-Time block of Server-Start opens the
	// stream, and the first HMAC, in Accept-Session, covers it too.
	block := append([]byte{0xe0}, make([]byte, 15)...)
	if err := server.Seal(block, false); err != nil || !bytes.Equal(block, v["serverstart_block"]) {
		t.Fatalf("server-start block = %x, %v", block, err)
	}
	acc := (&wire.AcceptSession{Port: 862, SID: vecSID}).Append(nil)
	if err := server.Seal(acc, true); err != nil || !bytes.Equal(acc, v["accept"]) {
		t.Fatalf("accept-session = %x, %v; want %x", acc, err, v["accept"])
	}
	if err := client.Open(block); err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{acc[:16], acc[16:]} {
		if err := client.Open(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Verify(acc); err != nil {
		t.Fatalf("client verify: %v", err)
	}
	var as wire.AcceptSession
	if err := as.Decode(acc); err != nil || as.Port != 862 || as.SID != vecSID {
		t.Errorf("accept-session %+v, %v", as, err)
	}
}

// TestControlCryptoStream sends several messages each way: the cipher chain
// and the HMAC stream carry over from message to message.
func TestControlCryptoStream(t *testing.T) {
	a := wire.NewControlCrypto(vecKeys, vecClientIV, vecServerIV)
	b := wire.NewControlCrypto(vecKeys, vecServerIV, vecClientIV)
	msgs := [][]byte{
		(&wire.StartSessions{}).Append(nil),
		(&wire.StopSessions{Sessions: 1}).Append(nil),
		(&wire.StartSessions{}).Append(nil), // same plaintext, other ciphertext
	}
	var sent [][]byte
	for _, m := range msgs {
		c := bytes.Clone(m)
		if err := a.Seal(c, true); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, c)
	}
	if bytes.Equal(sent[0], sent[2]) {
		t.Error("the cipher chain restarted between messages")
	}
	for i, c := range sent {
		if err := b.Open(c[:16]); err != nil {
			t.Fatal(err)
		}
		if err := b.Open(c[16:]); err != nil {
			t.Fatal(err)
		}
		if err := b.Verify(c); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if !bytes.Equal(c[:16], msgs[i][:16]) {
			t.Errorf("message %d decrypted to %x", i, c)
		}
	}
}

func TestControlCryptoRejects(t *testing.T) {
	seal := func(k wire.SessionKeys) []byte {
		m := (&wire.StartSessions{}).Append(nil)
		_ = wire.NewControlCrypto(k, vecClientIV, vecServerIV).Seal(m, true)
		return m
	}
	open := func(k wire.SessionKeys, m []byte) error {
		c := wire.NewControlCrypto(k, vecServerIV, vecClientIV)
		if err := c.Open(m); err != nil {
			return err
		}
		return c.Verify(m)
	}
	t.Run("flipped bit", func(t *testing.T) {
		m := seal(vecKeys)
		m[3] ^= 1
		if err := open(vecKeys, m); !errors.Is(err, wire.ErrIntegrity) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("other hmac key", func(t *testing.T) {
		other := vecKeys
		other.HMAC[0] ^= 1
		if err := open(other, seal(vecKeys)); !errors.Is(err, wire.ErrIntegrity) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("other aes key", func(t *testing.T) {
		other := vecKeys
		other.AES[0] ^= 1
		if err := open(other, seal(vecKeys)); !errors.Is(err, wire.ErrIntegrity) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("not blocks", func(t *testing.T) {
		c := wire.NewControlCrypto(vecKeys, vecClientIV, vecServerIV)
		if c.Seal(make([]byte, 20), true) == nil || c.Open(make([]byte, 20)) == nil || c.Seal(nil, false) == nil || c.Open(nil) == nil {
			t.Error("a length that is not whole blocks was accepted")
		}
		if c.Verify(make([]byte, 32)) == nil {
			t.Error("Verify without Open accepted")
		}
	})
}

func TestTestCryptoRejects(t *testing.T) {
	keys := wire.DeriveTestKeys(vecKeys, vecSID)
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted} {
		t.Run(mode.String(), func(t *testing.T) {
			c, _ := wire.NewTestCrypto(mode, keys)
			if c.Mode() != mode {
				t.Errorf("Mode = %v", c.Mode())
			}
			b := vecSenderPlain()
			_ = c.SealSender(b)
			b[len(b)-1] ^= 0xff // padding is not covered
			if err := c.OpenSender(bytes.Clone(b)); err != nil {
				t.Errorf("padding change rejected: %v", err)
			}
			b[1] ^= 1
			if err := c.OpenSender(b); !errors.Is(err, wire.ErrIntegrity) {
				t.Errorf("flipped sequence number: err = %v", err)
			}
			r := vecReflectorPlain()
			_ = c.SealReflector(r)
			r[100] ^= 1 // the HMAC field
			if err := c.OpenReflector(r); !errors.Is(err, wire.ErrIntegrity) {
				t.Errorf("flipped hmac: err = %v", err)
			}
			other := keys
			other.HMAC[5] ^= 1
			o, _ := wire.NewTestCrypto(mode, other)
			r = vecReflectorPlain()
			_ = c.SealReflector(r)
			if err := o.OpenReflector(r); !errors.Is(err, wire.ErrIntegrity) {
				t.Errorf("other key: err = %v", err)
			}
			short := make([]byte, wire.AuthSenderHeaderSize-1)
			if c.SealSender(short) == nil || c.OpenSender(short) == nil || c.SealReflector(make([]byte, 111)) == nil || c.OpenReflector(make([]byte, 111)) == nil {
				t.Error("short packet accepted")
			}
		})
	}
	for _, m := range []wire.SecurityMode{wire.SecurityUnauthenticated, wire.SecurityMixed, 0} {
		if _, err := wire.NewTestCrypto(m, keys); err == nil {
			t.Errorf("NewTestCrypto(%v) succeeded", m)
		}
	}
}

func TestTestCryptoAllocations(t *testing.T) {
	keys := wire.DeriveTestKeys(vecKeys, vecSID)
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted} {
		c, _ := wire.NewTestCrypto(mode, keys)
		s, r := vecSenderPlain(), vecReflectorPlain()
		n := testing.AllocsPerRun(100, func() {
			_ = c.SealSender(s)
			_ = c.OpenSender(s)
			_ = c.SealReflector(r)
			_ = c.OpenReflector(r)
		})
		if n != 0 {
			t.Errorf("%v: %v allocations per packet", mode, n)
		}
	}
}

func TestAuthReflect(t *testing.T) {
	cases := []struct {
		name      string
		n, reply  int
		padPrefix []byte
		dscpecn   bool
	}{
		{"no padding", 48, 112, nil, false},
		{"padding below the truncation", 48 + 40, 112, nil, false},
		{"equal size", 48 + 64, 112, nil, false},
		{"padding kept", 48 + 100, 148, seqBytes(36, 1), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := make([]byte, max(c.n, 112))
			s := wire.AuthSenderPacket{Seq: 9, Timestamp: goldenTime, ErrorEstimate: goldenEE}
			_ = s.PutHeader(b)
			copy(b[48:c.n], seqBytes(c.n-48, 1))
			p := wire.AuthReflectorPacket{Seq: 1, Timestamp: goldenTime + 2, ReceiveTimestamp: goldenTime + 1, SenderTTL: 254, HasDSCPECN: c.dscpecn, DSCPECN: 0xb8}
			n, err := p.Reflect(b, c.n)
			if err != nil || n != c.reply {
				t.Fatalf("Reflect = %d, %v; want %d", n, err, c.reply)
			}
			got := wire.AuthReflectorPacket{HasDSCPECN: c.dscpecn}
			if err := got.Decode(b[:n]); err != nil {
				t.Fatal(err)
			}
			want := wire.AuthReflectorPacket{
				Seq: 1, Timestamp: goldenTime + 2, ReceiveTimestamp: goldenTime + 1, SenderSeq: 9,
				SenderTimestamp: goldenTime, SenderErrorEstimate: goldenEE, SenderTTL: 254, HasDSCPECN: c.dscpecn,
			}
			if c.dscpecn {
				want.DSCPECN = 0xb8
			}
			got.Padding, want.Padding = nil, nil
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded %+v, want %+v", got, want)
			}
			if !bytes.Equal(b[112:n], c.padPrefix) {
				t.Errorf("padding %x, want %x", b[112:n], c.padPrefix)
			}
			if !c.dscpecn && b[81] != 0 || !bytes.Equal(b[82:96], make([]byte, 14)) {
				t.Errorf("MBZ octets set: %x", b[80:96])
			}
		})
	}
	var p wire.AuthReflectorPacket
	if _, err := p.Reflect(make([]byte, 111), 48); err == nil {
		t.Error("Reflect into a short buffer")
	}
	if _, err := p.Reflect(make([]byte, 112), 47); err == nil {
		t.Error("Reflect of a short packet")
	}
	if err := p.Decode(make([]byte, 111)); err == nil {
		t.Error("Decode of a short packet")
	}
	var s wire.AuthSenderPacket
	if s.Decode(make([]byte, 47)) == nil || s.PutHeader(make([]byte, 47)) == nil || wire.PutAuthTimestamp(make([]byte, 47), 0) == nil {
		t.Error("short sender packet accepted")
	}
	b := vecSenderPlain()
	if err := wire.PutAuthTimestamp(b, goldenTime); err != nil {
		t.Fatal(err)
	}
	if err := s.Decode(b); err != nil || s.Timestamp != goldenTime || s.Seq != 7 || s.ErrorEstimate != errest.Estimate(0x8001) || len(s.Padding) != 4 {
		t.Errorf("decoded %+v, %v", s, err)
	}
}

func TestKeyID(t *testing.T) {
	id, err := wire.KeyID("gotwamp")
	if err != nil || wire.KeyIDString(id) != "gotwamp" || id[7] != 0 {
		t.Errorf("KeyID = %q, %v", id, err)
	}
	full := strings.Repeat("k", 80)
	if id, err := wire.KeyID(full); err != nil || wire.KeyIDString(id) != full {
		t.Errorf("80-octet KeyID: %v", err)
	}
	for _, bad := range []string{"", full + "k", "a\x00b"} {
		if _, err := wire.KeyID(bad); err == nil {
			t.Errorf("KeyID(%q) accepted", bad)
		}
	}
}

func FuzzTestCryptoOpen(f *testing.F) {
	keys := wire.DeriveTestKeys(vecKeys, vecSID)
	f.Add(vecSenderPlain(), false)
	f.Add(vecReflectorPlain(), true)
	f.Fuzz(func(_ *testing.T, b []byte, encrypted bool) {
		mode := wire.SecurityAuthenticated
		if encrypted {
			mode = wire.SecurityEncrypted
		}
		c, _ := wire.NewTestCrypto(mode, keys)
		_ = c.OpenSender(bytes.Clone(b))
		_ = c.OpenReflector(bytes.Clone(b))
		var p wire.AuthReflectorPacket
		_, _ = p.Reflect(append(bytes.Clone(b), make([]byte, 112)...), len(b))
	})
}

func FuzzControlCryptoOpen(f *testing.F) {
	f.Add([]byte{}, 16)
	f.Add(make([]byte, 48), 16)
	f.Fuzz(func(t *testing.T, b []byte, split int) {
		c := wire.NewControlCrypto(vecKeys, vecServerIV, vecClientIV)
		split = max(0, min(split, len(b)))
		_ = c.Open(b[:split])
		_ = c.Open(b[split:])
		if c.Verify(b) == nil && len(b) > 0 {
			t.Fatalf("random octets %x passed the hmac check", b)
		}
	})
}

func TestNewSessionKeys(t *testing.T) {
	k, err := wire.NewSessionKeys(bytes.NewReader(seqBytes(48, 1)))
	if err != nil || k.AES != [16]byte(seqBytes(16, 1)) || k.HMAC != [32]byte(seqBytes(32, 17)) {
		t.Errorf("keys %+v, %v", k, err)
	}
	for _, n := range []int{0, 20} {
		if _, err := wire.NewSessionKeys(bytes.NewReader(make([]byte, n))); err == nil {
			t.Errorf("%d random octets were enough", n)
		}
	}
}
