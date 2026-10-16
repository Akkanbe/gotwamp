package gotwamp

// Layout sizes of TWAMP-Test packets, in octets. The authenticated sizes
// follow RFC 5357 as corrected by EID 5045 and EID 5046.
const (
	// SenderHeaderSize is the unauthenticated Session-Sender packet without padding.
	SenderHeaderSize = 14
	// ReflectorHeaderSize is the unauthenticated Session-Reflector packet without padding.
	ReflectorHeaderSize = 41
	// AuthSenderHeaderSize is the authenticated or encrypted Session-Sender packet without padding.
	AuthSenderHeaderSize = 48
	// AuthReflectorHeaderSize is the authenticated or encrypted Session-Reflector packet without padding.
	AuthReflectorHeaderSize = 112
	// DefaultPaddingLength makes unauthenticated packets the same size in both
	// directions (SPEC appendix A).
	DefaultPaddingLength = ReflectorHeaderSize - SenderHeaderSize
)

// SecurityMode is the security mode negotiated in Set-Up-Response: one of the
// bits 0–3 of Modes.
type SecurityMode uint8

// Security modes, with the value of their Modes bit.
const (
	SecurityUnauthenticated = SecurityMode(ModeUnauthenticated)
	SecurityAuthenticated   = SecurityMode(ModeAuthenticated)
	SecurityEncrypted       = SecurityMode(ModeEncrypted)
	SecurityMixed           = SecurityMode(ModeMixed) // RFC 5618: encrypted control, unauthenticated test
)

// Valid reports whether s is one of the four security modes.
func (s SecurityMode) Valid() bool {
	switch s {
	case SecurityUnauthenticated, SecurityAuthenticated, SecurityEncrypted, SecurityMixed:
		return true
	default:
		return false
	}
}

// Mode returns the Modes bit of s.
func (s SecurityMode) Mode() Modes { return Modes(s) }

// TestMode returns the mode TWAMP-Test packets use under s: mixed mode sends
// unauthenticated test packets.
func (s SecurityMode) TestMode() SecurityMode {
	if s == SecurityMixed {
		return SecurityUnauthenticated
	}
	return s
}

// SenderHeaderSize returns the Session-Sender packet size without padding, or
// 0 if s is not valid.
func (s SecurityMode) SenderHeaderSize() int {
	switch s.TestMode() {
	case SecurityUnauthenticated:
		return SenderHeaderSize
	case SecurityAuthenticated, SecurityEncrypted:
		return AuthSenderHeaderSize
	default:
		return 0
	}
}

// ReflectorHeaderSize returns the Session-Reflector packet size without
// padding, or 0 if s is not valid.
func (s SecurityMode) ReflectorHeaderSize() int {
	switch s.TestMode() {
	case SecurityUnauthenticated:
		return ReflectorHeaderSize
	case SecurityAuthenticated, SecurityEncrypted:
		return AuthReflectorHeaderSize
	default:
		return 0
	}
}

// PaddingTruncation returns how much larger the reflector header is than the
// sender header: the least padding that gives both directions the same size,
// and what the reflector drops from the end of the padding (27 or 64).
func (s SecurityMode) PaddingTruncation() int {
	return s.ReflectorHeaderSize() - s.SenderHeaderSize()
}

// String returns the mode name.
func (s SecurityMode) String() string {
	if !s.Valid() {
		return "SecurityMode(" + Modes(s).String() + ")"
	}
	return Modes(s).String()
}
