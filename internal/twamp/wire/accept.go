package wire

import "strconv"

// Accept is the Accept field of Server-Start, Accept-Session, Start-Ack and
// Stop-Sessions (RFC 4656 §3.3, adopted by RFC 5357 §3.3).
type Accept uint8

// Accept values.
const (
	AcceptOK             Accept = 0 // OK
	AcceptFailure        Accept = 1 // failure, reason unspecified
	AcceptInternalError  Accept = 2 // internal error
	AcceptNotSupported   Accept = 3 // some aspect of the request is not supported
	AcceptPermanentLimit Accept = 4 // permanent resource limitation
	AcceptTemporaryLimit Accept = 5 // temporary resource limitation
)

var acceptNames = [...]string{
	AcceptOK:             "OK",
	AcceptFailure:        "failure",
	AcceptInternalError:  "internal error",
	AcceptNotSupported:   "not supported",
	AcceptPermanentLimit: "permanent resource limitation",
	AcceptTemporaryLimit: "temporary resource limitation",
}

// Normalize maps the reserved values to AcceptFailure, which is how RFC 4656
// §3.3 tells receivers to read them. Decoders keep the raw value so it can be
// reported; compare against the result of Normalize.
func (a Accept) Normalize() Accept {
	if int(a) < len(acceptNames) {
		return a
	}
	return AcceptFailure
}

// String describes a, keeping the raw number for reserved values.
func (a Accept) String() string {
	if int(a) < len(acceptNames) {
		return acceptNames[a]
	}
	return "Accept(" + strconv.Itoa(int(a)) + ")"
}
