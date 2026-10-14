package wire

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

// ServerStartSize is the length of a Server-Start.
const ServerStartSize = 48

var errShortServerStart = fmt.Errorf("server-start: %w", ErrShortBuffer)

// ServerStart is the server's answer to Set-Up-Response (RFC 4656 §3.1).
//
//	0–14 MBZ, 15 Accept, 16 Server-IV, 32 Start-Time, 40–47 MBZ
//
// In the encrypted control modes octets 32–47 are the first block of the
// server's stream; that layer works on the encoded bytes.
type ServerStart struct {
	// Accept is AcceptOK when the server accepts the chosen mode.
	Accept Accept
	// ServerIV is the IV of the server's control stream.
	ServerIV [16]byte
	// StartTime is when the server instance started. Set it to zero when
	// Accept is not AcceptOK (RFC 4656 §3.1).
	StartTime ntptime.Timestamp
}

// NewServerStart returns a Server-Start with ServerIV read from rand. A
// rejecting Accept clears startTime.
func NewServerStart(accept Accept, startTime ntptime.Timestamp, rand io.Reader) (ServerStart, error) {
	if accept != AcceptOK {
		startTime = 0
	}
	s := ServerStart{Accept: accept, StartTime: startTime}
	if _, err := io.ReadFull(rand, s.ServerIV[:]); err != nil {
		return ServerStart{}, fmt.Errorf("server-start iv: %w", err)
	}
	return s, nil
}

// Append appends the encoded message to dst.
func (m *ServerStart) Append(dst []byte) []byte {
	var b [ServerStartSize]byte
	b[15] = byte(m.Accept)
	copy(b[16:32], m.ServerIV[:])
	binary.BigEndian.PutUint64(b[32:], uint64(m.StartTime))
	return append(dst, b[:]...)
}

// Decode decodes a Server-Start from the first ServerStartSize octets of b.
func (m *ServerStart) Decode(b []byte) error {
	if len(b) < ServerStartSize {
		return errShortServerStart
	}
	m.Accept = Accept(b[15])
	copy(m.ServerIV[:], b[16:32])
	m.StartTime = ntptime.Timestamp(binary.BigEndian.Uint64(b[32:]))
	return nil
}
