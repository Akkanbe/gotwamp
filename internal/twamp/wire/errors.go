package wire

import "errors"

//declscope:ignore overexported // sentinels the decoders wrap, for errors.Is; the tests match them
var (
	// ErrShortBuffer is returned when a buffer is shorter than the message or
	// packet it should hold.
	ErrShortBuffer = errors.New("short buffer")
	// ErrUnexpectedCommand is returned when a client command does not carry the
	// Command Number its message type requires.
	ErrUnexpectedCommand = errors.New("unexpected command number")
)
