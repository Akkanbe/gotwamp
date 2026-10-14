package wire

import (
	"encoding/binary"
	"fmt"
)

// SetUpResponseSize is the length of a Set-Up-Response.
const SetUpResponseSize = 164

var errShortSetUpResponse = fmt.Errorf("set-up-response: %w", ErrShortBuffer)

// SetUpResponse is the client's answer to the Server Greeting (RFC 4656 §3.1).
// It is always sent in the clear.
//
//	0 Mode, 4 KeyID, 84 Token, 148 Client-IV
type SetUpResponse struct {
	// Mode is the chosen security mode plus any feature bits; 0 means the
	// client will not continue.
	Mode Modes
	// KeyID selects the shared secret; zero in unauthenticated mode.
	KeyID [80]byte
	// Token is the encrypted Challenge and session keys; zero in unauthenticated mode.
	Token [64]byte
	// ClientIV is the IV of the client's control stream; zero in unauthenticated mode.
	ClientIV [16]byte
}

// Append appends the encoded response to dst.
func (m *SetUpResponse) Append(dst []byte) []byte {
	var b [SetUpResponseSize]byte
	binary.BigEndian.PutUint32(b[0:], uint32(m.Mode))
	copy(b[4:84], m.KeyID[:])
	copy(b[84:148], m.Token[:])
	copy(b[148:164], m.ClientIV[:])
	return append(dst, b[:]...)
}

// Decode decodes a response from the first SetUpResponseSize octets of b.
func (m *SetUpResponse) Decode(b []byte) error {
	if len(b) < SetUpResponseSize {
		return errShortSetUpResponse
	}
	m.Mode = Modes(binary.BigEndian.Uint32(b[0:]))
	copy(m.KeyID[:], b[4:84])
	copy(m.Token[:], b[84:148])
	copy(m.ClientIV[:], b[148:164])
	return nil
}
