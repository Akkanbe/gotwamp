// Package wire encodes and decodes TWAMP messages (RFC 5357, RFC 4656) and
// holds the cryptography of the authenticated, encrypted and mixed modes
// (RFC 4656 §3.1–§4, RFC 5618).
//
// The control messages are encoded in plaintext with zero HMAC fields; every
// HMAC field is the last 16 octets of its message. ControlSecurity is the
// layer between those plaintexts and the connection: Unauthenticated leaves
// them as they are, ControlCrypto computes the HMACs and runs the AES-CBC
// streams. TWAMP-Test packets have an unauthenticated layout (SenderPacket,
// ReflectorPacket) and an authenticated one (AuthSenderPacket,
// AuthReflectorPacket), which TestCrypto seals and opens.
//
// Encoders have the form Append(dst []byte) []byte and do not allocate when
// dst has room. Decoders have the form Decode(b []byte) error, read only the
// octets the message defines, ignore MBZ and HMAC fields as RFC 4656 §3.1
// requires, and never retain b except where a Padding field aliases it. The
// test-packet paths, cryptography included, do not allocate.
//
// The package uses no I/O, no clock and no global randomness: constructors
// that need random octets take an io.Reader.
package wire
