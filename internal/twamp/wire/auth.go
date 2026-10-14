package wire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 and PBKDF2-HMAC-SHA1 are what RFC 4656 §3.1 and §3.2 prescribe
	"errors"
	"fmt"
	"hash"
	"io"
)

// This file is the key hierarchy and the TWAMP-Control stream protection of
// the authenticated, encrypted and mixed modes (RFC 4656 §3.1–§3.4,
// RFC 5357 §3.1–§3.2, RFC 5618).

// Sizes of the security fields, in octets.
const (
	// keyIDSize is the KeyID field of Set-Up-Response.
	keyIDSize = 80
	// tokenSize is the Token field of Set-Up-Response.
	tokenSize = 64
	// IVSize is the Client-IV and Server-IV fields.
	IVSize = 16
	// derivedKeySize is the PBKDF2 output, an AES-128 key. RFC 4656 leaves
	// dkLen unstated; the reference implementation uses 16 (research §3.1).
	derivedKeySize = 16
)

// SessionKeys are the keys a Control-Client draws for one control connection
// and sends to the server inside the Token (RFC 4656 §3.1).
type SessionKeys struct {
	// AES encrypts the control streams and seeds the test-session keys.
	AES [16]byte
	// HMAC authenticates the control streams and seeds the test-session keys.
	HMAC [32]byte
}

// NewSessionKeys draws independent session keys from rand.
func NewSessionKeys(rand io.Reader) (SessionKeys, error) {
	var k SessionKeys
	if _, err := io.ReadFull(rand, k.AES[:]); err != nil {
		return SessionKeys{}, fmt.Errorf("session keys: %w", err)
	}
	if _, err := io.ReadFull(rand, k.HMAC[:]); err != nil {
		return SessionKeys{}, fmt.Errorf("session keys: %w", err)
	}
	return k, nil
}

// KeyID encodes id for the KeyID field: UTF-8, at most keyIDSize octets,
// zero padded. It must not contain a zero octet, which would end it early.
func KeyID(id string) ([keyIDSize]byte, error) {
	var b [keyIDSize]byte
	switch {
	case id == "":
		return b, errors.New("key id is empty")
	case len(id) > keyIDSize:
		return b, fmt.Errorf("key id is %d octets, above %d", len(id), keyIDSize)
	case bytes.IndexByte([]byte(id), 0) >= 0:
		return b, errors.New("key id contains a zero octet")
	}
	copy(b[:], id)
	return b, nil
}

// KeyIDString returns the identity in a KeyID field, without its zero padding.
func KeyIDString(b [keyIDSize]byte) string {
	if i := bytes.IndexByte(b[:], 0); i >= 0 {
		return string(b[:i])
	}
	return string(b[:])
}

// DeriveKey derives the key that encrypts the Token from the shared secret,
// with the Salt and Count of the Server Greeting: PBKDF2 with HMAC-SHA1 and
// a 16-octet output (RFC 4656 §3.1).
func DeriveKey(passphrase []byte, salt [16]byte, count uint32) ([16]byte, error) {
	var key [16]byte
	if count == 0 {
		return key, errors.New("derive key: count is 0")
	}
	dk, err := pbkdf2.Key(sha1.New, string(passphrase), salt[:], int(count), derivedKeySize)
	if err != nil {
		return key, fmt.Errorf("derive key: %w", err)
	}
	copy(key[:], dk)
	return key, nil
}

// EncryptToken returns the Token of Set-Up-Response: Challenge, the AES
// session key and the HMAC session key, encrypted with AES-CBC under key
// with a zero IV (RFC 4656 §3.1).
func EncryptToken(key, challenge [16]byte, k SessionKeys) [tokenSize]byte {
	var t [tokenSize]byte
	copy(t[0:16], challenge[:])
	copy(t[16:32], k.AES[:])
	copy(t[32:64], k.HMAC[:])
	// The key is 16 octets: NewCipher cannot fail. RFC 4656 §3.1 fixes the
	// IV to zero; the key is fresh with every greeting's Salt.
	block, _ := aes.NewCipher(key[:])
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(t[:], t[:]) //nolint:gosec // zero IV by RFC 4656 §3.1
	return t
}

// DecryptToken recovers the Challenge and the session keys from a Token. The
// server compares the Challenge with the one it sent: a different value
// means the client does not know the shared secret.
func DecryptToken(key [16]byte, token [tokenSize]byte) (challenge [16]byte, k SessionKeys) {
	block, _ := aes.NewCipher(key[:]) // cannot fail: the key is 16 octets
	cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(token[:], token[:])
	copy(challenge[:], token[0:16])
	copy(k.AES[:], token[16:32])
	copy(k.HMAC[:], token[32:64])
	return challenge, k
}

// ControlCrypto is the ControlSecurity of the authenticated, encrypted and
// mixed modes, which protect TWAMP-Control alike (RFC 4656 §3.4, RFC 5618
// §3.1). Each direction is one AES-CBC stream under the AES session key,
// with its own IV and chaining carried from message to message, and one
// HMAC-SHA1 stream under the HMAC session key: every HMAC covers what was
// sent in that direction since the previous HMAC, truncated to 128 bits
// (RFC 4656 §3.2). Authentication comes before encryption, and the HMAC
// fields are encrypted too (RFC 5357 §3.2).
//
// A ControlCrypto serves one connection and one goroutine.
type ControlCrypto struct {
	enc, dec         cipher.BlockMode
	sendMAC, recvMAC hash.Hash
	// held is the last block opened, kept out of recvMAC until the next
	// Open shows it is not an HMAC field: a reader only learns which block
	// holds the HMAC when Verify comes.
	held    [aes.BlockSize]byte
	holding bool
}

var _ ControlSecurity = (*ControlCrypto)(nil)

// NewControlCrypto returns the protection of one end of a control
// connection: sendIV is the IV of the stream this end sends (Client-IV for
// the client, Server-IV for the server) and recvIV the other one.
func NewControlCrypto(k SessionKeys, sendIV, recvIV [IVSize]byte) *ControlCrypto {
	block, _ := aes.NewCipher(k.AES[:]) // cannot fail: the key is 16 octets
	return &ControlCrypto{
		enc:     cipher.NewCBCEncrypter(block, sendIV[:]),
		dec:     cipher.NewCBCDecrypter(block, recvIV[:]),
		sendMAC: hmac.New(sha1.New, k.HMAC[:]),
		recvMAC: hmac.New(sha1.New, k.HMAC[:]),
	}
}

var errNotBlocks = errors.New("control security: length is not a multiple of 16 octets")

// Seal authenticates and encrypts msg in place. With hasHMAC its last
// 16 octets receive the HMAC of the stream since the previous HMAC.
func (c *ControlCrypto) Seal(msg []byte, hasHMAC bool) error {
	if len(msg)%aes.BlockSize != 0 || len(msg) == 0 || hasHMAC && len(msg) < hmacSize+aes.BlockSize {
		return errNotBlocks
	}
	if hasHMAC {
		body := len(msg) - hmacSize
		c.sendMAC.Write(msg[:body])
		var sum [sha1.Size]byte
		copy(msg[body:], c.sendMAC.Sum(sum[:0]))
		c.sendMAC.Reset()
	} else {
		c.sendMAC.Write(msg)
	}
	c.enc.CryptBlocks(msg, msg)
	return nil
}

// Open decrypts b in place and adds the plaintext to the receiving HMAC
// stream, holding back its last block until it is known not to be an HMAC
// field.
func (c *ControlCrypto) Open(b []byte) error {
	if len(b)%aes.BlockSize != 0 || len(b) == 0 {
		return errNotBlocks
	}
	c.dec.CryptBlocks(b, b)
	if c.holding {
		c.recvMAC.Write(c.held[:])
	}
	last := len(b) - aes.BlockSize
	c.recvMAC.Write(b[:last])
	copy(c.held[:], b[last:])
	c.holding = true
	return nil
}

// Verify checks the HMAC field of msg, the block Open received last, against
// the stream before it. A mismatch wraps ErrIntegrity.
func (c *ControlCrypto) Verify(msg []byte) error {
	if len(msg) < hmacSize || !c.holding {
		return fmt.Errorf("control security: no hmac field to verify: %w", ErrIntegrity)
	}
	var sum [sha1.Size]byte
	got := c.recvMAC.Sum(sum[:0])
	c.recvMAC.Reset()
	c.holding = false
	if !hmac.Equal(got[:hmacSize], msg[len(msg)-hmacSize:]) {
		return fmt.Errorf("control message hmac mismatch: %w", ErrIntegrity)
	}
	return nil
}
