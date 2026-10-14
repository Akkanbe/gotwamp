package wire

import "strconv"

// ControlHeaderSize is the number of octets a reader needs to see before it
// knows the type, and so the length, of any TWAMP-Control message
// (RFC 4656 §3).
const ControlHeaderSize = 16

// Command is a TWAMP-Control Command Number (IANA "TWAMP-Control Command
// Numbers", 0–255 per RFC 5357 EID 1593).
type Command uint8

// Command Numbers registered for TWAMP.
//
//declscope:ignore overexported // the whole IANA registry, so that String names every command a peer sends
const (
	CommandReserved               Command = 0  // RFC 5357
	CommandForbidden              Command = 1  // OWAMP Request-Session, RFC 5357
	CommandStartSessions          Command = 2  // RFC 4656 §3.7
	CommandStopSessions           Command = 3  // RFC 5357 §3.8
	CommandFetchSession           Command = 4  // reserved in TWAMP, RFC 5357 §3.9
	CommandRequestTWSession       Command = 5  // RFC 5357 §3.5
	CommandExperimentation        Command = 6  // RFC 5357 §8.3
	CommandStartNSessions         Command = 7  // RFC 5938 §3.2
	CommandStartNAck              Command = 8  // RFC 5938 §3.3
	CommandStopNSessions          Command = 9  // RFC 5938 §3.4
	CommandStopNAck               Command = 10 // RFC 5938 §3.5
	CommandRequestTWMicroSessions Command = 11 // RFC 9533 §4.1
)

var commandNames = [...]string{
	CommandReserved:               "Reserved",
	CommandForbidden:              "Forbidden",
	CommandStartSessions:          "Start-Sessions",
	CommandStopSessions:           "Stop-Sessions",
	CommandFetchSession:           "Fetch-Session",
	CommandRequestTWSession:       "Request-TW-Session",
	CommandExperimentation:        "Experimentation",
	CommandStartNSessions:         "Start-N-Sessions",
	CommandStartNAck:              "Start-N-Ack",
	CommandStopNSessions:          "Stop-N-Sessions",
	CommandStopNAck:               "Stop-N-Ack",
	CommandRequestTWMicroSessions: "Request-TW-Micro-Sessions",
}

// String returns the registry name of c, or "Command(n)" if it is unassigned.
func (c Command) String() string {
	if int(c) < len(commandNames) {
		return commandNames[c]
	}
	return "Command(" + strconv.Itoa(int(c)) + ")"
}

// MessageSize returns the length of the client message that c starts, or 0
// for a command this package does not decode.
func (c Command) MessageSize() int {
	switch c {
	case CommandStartSessions:
		return StartSessionsSize
	case CommandStopSessions:
		return StopSessionsSize
	case CommandRequestTWSession:
		return RequestTWSessionSize
	default:
		return 0
	}
}
