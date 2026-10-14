package wire

import "fmt"

// PaddingLayout describes the test packets of a session under RFC 6038
// Reflect Octets (Modes bit 5) and Symmetrical Size (Modes bit 6), from the
// Request-TW-Session fields and the Modes negotiated. The zero value is the
// plain RFC 5357 layout, where all the padding is ordinary padding.
//
// Padding Length counts the same octets in every layout: those after the
// Session-Sender header (RFC 6038 §4.2). Under Symmetrical Size the header
// grows by the MBZ octets that stand in for the larger reflector header, 27
// or 64 when authenticated (RFC 6038 §5.1.4, EID 5549); the reflector then
// keeps the padding in place, so both packets have the same length
// (§5.2.2). Without it, the reflector drops the last 27 or 64 octets of the
// padding (RFC 5357 §4.2.1, RFC 6038 §5.2.1, EID 6410).
type PaddingLayout struct {
	// SymmetricalSize is set when Symmetrical Size is in use.
	SymmetricalSize bool
	// ReflectLength is "Length of padding to reflect" (Reflect Octets): the
	// octets at the start of the padding that the reflector returns
	// verbatim at the start of its own.
	ReflectLength uint16
}

// SenderHeaderSize returns the Session-Sender header of mode under l: the
// header of the security mode, plus the MBZ octets of Symmetrical Size.
func (l PaddingLayout) SenderHeaderSize(mode SecurityMode) int {
	if l.SymmetricalSize {
		return mode.SenderHeaderSize() + mode.PaddingTruncation()
	}
	return mode.SenderHeaderSize()
}

// Sender splits the Packet Padding of a Session-Sender packet, the octets
// after SenderHeaderSize, into the octets to be reflected and the additional
// padding. Parts the padding is too short for are shortened. The slices
// alias padding.
func (l PaddingLayout) Sender(padding []byte) (reflected, additional []byte) {
	return cut(padding, int(l.ReflectLength))
}

// Reflector splits the Packet Padding of a Session-Reflector packet into the
// reflected octets and the additional padding (RFC 6038 §5.2.1). The
// slices alias padding.
func (l PaddingLayout) Reflector(padding []byte) (reflected, additional []byte) {
	return cut(padding, int(l.ReflectLength))
}

// Validate checks a Padding Length against the layout in mode (RFC 6038
// §4.2, EID 6409): with Reflect Octets it must exceed the length to reflect
// and, where the reflector truncates, also leave the truncated octets. An
// error means Accept 3.
func (l PaddingLayout) Validate(mode SecurityMode, paddingLength uint32) error {
	if l.ReflectLength == 0 {
		return nil
	}
	least := uint32(l.ReflectLength) + 1
	if !l.SymmetricalSize {
		least = max(least, uint32(l.ReflectLength)+uint32(mode.PaddingTruncation())) //nolint:gosec // G115: 27 or 64
	}
	if paddingLength < least {
		return fmt.Errorf("padding %d is too short to reflect %d octets (at least %d)", paddingLength, l.ReflectLength, least)
	}
	return nil
}

// cut splits b after n octets, or at its end when it is shorter.
func cut(b []byte, n int) (head, tail []byte) {
	n = min(n, len(b))
	return b[:n:n], b[n:]
}
