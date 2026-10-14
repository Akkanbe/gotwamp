package wire

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ServerGreetingSize is the length of a Server Greeting.
const ServerGreetingSize = 64

// Bounds of the Server Greeting Count field (RFC 4656 §3.1, RFC 5357 §6).
const (
	// GreetingCountMin is the least Count a server may send.
	GreetingCountMin = 1024
	// GreetingCountDefault is the Count gotwampd sends (SPEC appendix A).
	GreetingCountDefault = 2048
	// GreetingCountMax is the largest Count a client accepts by default.
	GreetingCountMax = 32768
)

var errShortServerGreeting = fmt.Errorf("server greeting: %w", ErrShortBuffer)

// ServerGreeting is the first message of a control connection, sent by the
// Server (RFC 4656 §3.1).
//
//	0–11 Unused, 12 Modes, 16 Challenge, 32 Salt, 48 Count, 52–63 MBZ
type ServerGreeting struct {
	// Modes are the modes the server offers; 0 means it refuses the client.
	Modes Modes
	// Challenge is a fresh random value, used by the authenticated modes.
	Challenge [16]byte
	// Salt is a random value for key derivation.
	Salt [16]byte
	// Count is the PBKDF2 iteration count: a power of 2, at least 1024.
	Count uint32
}

// NewServerGreeting returns a greeting offering modes, with Challenge and Salt
// read from rand.
func NewServerGreeting(modes Modes, count uint32, rand io.Reader) (ServerGreeting, error) {
	g := ServerGreeting{Modes: modes, Count: count}
	if _, err := io.ReadFull(rand, g.Challenge[:]); err != nil {
		return ServerGreeting{}, fmt.Errorf("server greeting challenge: %w", err)
	}
	if _, err := io.ReadFull(rand, g.Salt[:]); err != nil {
		return ServerGreeting{}, fmt.Errorf("server greeting salt: %w", err)
	}
	return g, nil
}

// Append appends the encoded greeting to dst.
func (m *ServerGreeting) Append(dst []byte) []byte {
	var b [ServerGreetingSize]byte
	binary.BigEndian.PutUint32(b[12:], uint32(m.Modes))
	copy(b[16:32], m.Challenge[:])
	copy(b[32:48], m.Salt[:])
	binary.BigEndian.PutUint32(b[48:], m.Count)
	return append(dst, b[:]...)
}

// Decode decodes a greeting from the first ServerGreetingSize octets of b.
func (m *ServerGreeting) Decode(b []byte) error {
	if len(b) < ServerGreetingSize {
		return errShortServerGreeting
	}
	m.Modes = Modes(binary.BigEndian.Uint32(b[12:]))
	copy(m.Challenge[:], b[16:32])
	copy(m.Salt[:], b[32:48])
	m.Count = binary.BigEndian.Uint32(b[48:])
	return nil
}
