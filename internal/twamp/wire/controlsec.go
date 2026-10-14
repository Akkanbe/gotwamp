package wire

import "errors"

// ErrIntegrity is returned by ControlSecurity.Verify when a message fails
// its HMAC check; the connection must then be dropped (RFC 4656 §6.10).
var ErrIntegrity = errors.New("control message failed its integrity check")

// ControlSecurity is the layer between the plaintext TWAMP-Control messages
// this package encodes and the octets on the connection (SPEC §13).
//
// In the authenticated, encrypted and mixed modes it computes and checks the
// HMAC of each message (HMAC-SHA1 truncated to 128 bits over the stream
// since the previous HMAC, RFC 4656 §3.2) and runs one AES-CBC stream per
// direction (RFC 4656 §3.4). In unauthenticated mode it changes nothing
// (Unauthenticated). The Server Greeting and Set-Up-Response always go in
// the clear and never pass through it; of Server-Start, only octets 32–47
// (the Start-Time block) do.
//
// Calls follow the order of the messages on the connection, since both the
// cipher chaining and the HMAC cover the stream as a whole.
type ControlSecurity interface {
	// Seal turns the plaintext of an outgoing message into the octets to
	// send, in place. When hasHMAC is set, the last 16 octets are the
	// HMAC field, which Seal fills before encrypting.
	Seal(msg []byte, hasHMAC bool) error
	// Open decrypts received octets in place. A reader learns the length of
	// a message from its first ControlHeaderSize octets, so it opens those
	// first and the rest of the message in a second call.
	Open(b []byte) error
	// Verify checks the HMAC field, the last 16 octets, of a received
	// message that Open has decrypted. It returns an error wrapping
	// ErrIntegrity when the check fails.
	Verify(msg []byte) error
}

// Unauthenticated is the ControlSecurity of unauthenticated mode, where the
// control messages travel in the clear with zero HMAC fields that receivers
// do not check (RFC 4656 §3.1).
type Unauthenticated struct{}

// Seal leaves msg unchanged; its HMAC field stays zero.
func (Unauthenticated) Seal([]byte, bool) error { return nil }

// Open leaves b unchanged.
func (Unauthenticated) Open([]byte) error { return nil }

// Verify accepts every message.
func (Unauthenticated) Verify([]byte) error { return nil }
