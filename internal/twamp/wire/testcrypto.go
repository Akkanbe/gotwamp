package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is what RFC 4656 §4.1.2 prescribes
	"crypto/subtle"
	"fmt"
	"hash"
)

// This file is the cryptography of authenticated and encrypted TWAMP-Test
// packets (RFC 4656 §4.1.2, RFC 5357 §4.1.2 and §4.2.1).
//
//	mode           Session-Sender packet          Session-Reflector packet
//	authenticated  HMAC and AES-ECB over 0–15     HMAC and AES-ECB over 0–15
//	encrypted      HMAC and AES-CBC over 0–31     HMAC and AES-CBC over 0–95
//
// The CBC IV is zero and chaining does not span packets. The HMAC is
// HMAC-SHA1 truncated to 16 octets, computed over the plaintext and never
// encrypted; the padding is neither covered nor encrypted.

// Octets of the test packets that the HMAC covers and the cipher encrypts.
const (
	authCovered         = 16 // one block, in both directions, authenticated mode
	encSenderCovered    = 32 // blocks 0–1 of a Session-Sender packet, encrypted mode
	encReflectorCovered = 96 // blocks 0–5 of a Session-Reflector packet, encrypted mode
	authSenderHMACAt    = 32
	authReflectorHMACAt = 96
)

// TestKeys are the keys of one test session, derived from the control
// session keys and the SID (RFC 5357 §4.2.1).
type TestKeys struct {
	AES  [16]byte
	HMAC [32]byte
}

// DeriveTestKeys derives the keys of the test session sid: the AES key is
// the control AES key encrypted with AES-ECB under the SID, and the HMAC key
// is the control HMAC key encrypted with AES-CBC under the SID with a zero
// IV (RFC 5357 §4.2.1).
func DeriveTestKeys(k SessionKeys, sid SID) TestKeys {
	block, _ := aes.NewCipher(sid[:]) // cannot fail: a SID is 16 octets
	var t TestKeys
	block.Encrypt(t.AES[:], k.AES[:])
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(t.HMAC[:], k.HMAC[:]) //nolint:gosec // zero IV by RFC 5357 §4.2.1
	return t
}

// TestCrypto protects the test packets of one session in authenticated or
// encrypted mode. It keeps the key schedule of the session, set up once
// (RFC 5357 §4.2.1), and works without allocating. It serves one goroutine:
// a Session-Sender that sends and receives in two needs two.
type TestCrypto struct {
	mode  SecurityMode
	block cipher.Block
	mac   hash.Hash
	sum   [sha1.Size]byte
	prev  [aes.BlockSize]byte
	next  [aes.BlockSize]byte
}

// NewTestCrypto returns the protection of the test packets of a session in
// mode, which must be SecurityAuthenticated or SecurityEncrypted.
func NewTestCrypto(mode SecurityMode, k TestKeys) (*TestCrypto, error) {
	if mode != SecurityAuthenticated && mode != SecurityEncrypted {
		return nil, fmt.Errorf("test packets of %v mode carry no cryptography", mode)
	}
	block, _ := aes.NewCipher(k.AES[:]) // cannot fail: the key is 16 octets
	return &TestCrypto{mode: mode, block: block, mac: hmac.New(sha1.New, k.HMAC[:])}, nil
}

// Mode returns the security mode of the packets.
func (c *TestCrypto) Mode() SecurityMode { return c.mode }

// Errors for test packets too short for the authenticated layouts.
//
//declscope:package // the layouts in security.go and their cryptography here
var (
	errShortAuthSender    = fmt.Errorf("authenticated sender packet: %w", ErrShortBuffer)
	errShortAuthReflector = fmt.Errorf("authenticated reflector packet: %w", ErrShortBuffer)
)

var errTestHMAC = fmt.Errorf("test packet hmac mismatch: %w", ErrIntegrity)

// SealSender fills the HMAC of the Session-Sender packet plaintext in b and
// encrypts it in place.
func (c *TestCrypto) SealSender(b []byte) error {
	if len(b) < AuthSenderHeaderSize {
		return errShortAuthSender
	}
	c.seal(b, c.covered(encSenderCovered), authSenderHMACAt)
	return nil
}

// OpenSender decrypts the Session-Sender packet in b in place and checks its
// HMAC; a mismatch wraps ErrIntegrity.
func (c *TestCrypto) OpenSender(b []byte) error {
	if len(b) < AuthSenderHeaderSize {
		return errShortAuthSender
	}
	return c.open(b, c.covered(encSenderCovered), authSenderHMACAt)
}

// SealReflector fills the HMAC of the Session-Reflector packet plaintext in b
// and encrypts it in place.
func (c *TestCrypto) SealReflector(b []byte) error {
	if len(b) < AuthReflectorHeaderSize {
		return errShortAuthReflector
	}
	c.seal(b, c.covered(encReflectorCovered), authReflectorHMACAt)
	return nil
}

// OpenReflector decrypts the Session-Reflector packet in b in place and
// checks its HMAC; a mismatch wraps ErrIntegrity.
func (c *TestCrypto) OpenReflector(b []byte) error {
	if len(b) < AuthReflectorHeaderSize {
		return errShortAuthReflector
	}
	return c.open(b, c.covered(encReflectorCovered), authReflectorHMACAt)
}

// covered returns how many leading octets the mode protects: one block when
// authenticated, encrypted otherwise.
func (c *TestCrypto) covered(encrypted int) int {
	if c.mode == SecurityAuthenticated {
		return authCovered
	}
	return encrypted
}

func (c *TestCrypto) seal(b []byte, covered, hmacAt int) {
	copy(b[hmacAt:hmacAt+hmacSize], c.hmac(b[:covered]))
	c.encrypt(b[:covered])
}

func (c *TestCrypto) open(b []byte, covered, hmacAt int) error {
	c.decrypt(b[:covered])
	if !hmac.Equal(c.hmac(b[:covered]), b[hmacAt:hmacAt+hmacSize]) {
		return errTestHMAC
	}
	return nil
}

// hmac returns the truncated HMAC of b, valid until the next call.
func (c *TestCrypto) hmac(b []byte) []byte {
	c.mac.Reset()
	c.mac.Write(b)
	return c.mac.Sum(c.sum[:0])[:hmacSize]
}

// encrypt runs AES over b: ECB for one block, CBC with a zero IV otherwise.
// It is written out to stay free of allocation.
func (c *TestCrypto) encrypt(b []byte) {
	c.prev = [aes.BlockSize]byte{}
	for i := 0; i < len(b); i += aes.BlockSize {
		blk := b[i : i+aes.BlockSize]
		subtle.XORBytes(blk, blk, c.prev[:])
		c.block.Encrypt(blk, blk)
		copy(c.prev[:], blk)
	}
}

func (c *TestCrypto) decrypt(b []byte) {
	c.prev = [aes.BlockSize]byte{}
	for i := 0; i < len(b); i += aes.BlockSize {
		blk := b[i : i+aes.BlockSize]
		copy(c.next[:], blk)
		c.block.Decrypt(blk, blk)
		subtle.XORBytes(blk, blk, c.prev[:])
		c.prev = c.next
	}
}
