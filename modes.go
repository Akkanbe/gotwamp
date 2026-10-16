package gotwamp

import (
	"math/bits"
	"strconv"
	"strings"
)

// Modes is the Modes field of Server Greeting and the Mode field of
// Set-Up-Response (IANA "TWAMP-Modes", indexed by bit position).
type Modes uint32

// Mode bits.
//
//declscope:ignore overexported // the whole IANA registry, so that String names every bit a peer offers
const (
	ModeUnauthenticated          Modes = 1 << 0 // RFC 4656 §3.1
	ModeAuthenticated            Modes = 1 << 1 // RFC 4656 §3.1
	ModeEncrypted                Modes = 1 << 2 // RFC 4656 §3.1
	ModeMixed                    Modes = 1 << 3 // unauthenticated test, encrypted control, RFC 5618
	ModeIndividualSessionControl Modes = 1 << 4 // RFC 5938
	ModeReflectOctets            Modes = 1 << 5 // RFC 6038
	ModeSymmetricalSize          Modes = 1 << 6 // RFC 6038
	ModeIKEv2Derived             Modes = 1 << 7 // RFC 7717
	ModeDSCPECNMonitoring        Modes = 1 << 8 // RFC 7750
	ModePTPv2Timestamp           Modes = 1 << 9 // RFC 8186
)

// SecurityModes are the security mode bits (0–3): a server offers any of
// them, and a Set-Up-Response carries exactly one.
const SecurityModes = ModeUnauthenticated | ModeAuthenticated | ModeEncrypted | ModeMixed

var modeNames = [...]string{
	"unauthenticated",
	"authenticated",
	"encrypted",
	"mixed",
	"individual-session-control",
	"reflect-octets",
	"symmetrical-size",
	"ikev2-derived",
	"dscp-ecn-monitoring",
	"ptpv2-timestamp",
}

// Has reports whether every bit of want is set in m. Other bits, including
// unassigned ones, are ignored as RFC 4656 §3.1 requires.
func (m Modes) Has(want Modes) bool { return m&want == want }

// Security returns the security mode selected by m, and false unless exactly
// one of the security bits (0–3) is set.
func (m Modes) Security() (SecurityMode, bool) {
	s := m & SecurityModes
	if bits.OnesCount32(uint32(s)) != 1 {
		return 0, false
	}
	return SecurityMode(s), true
}

// String lists the set bits by name, joined with "|". Unassigned bits are
// shown as one hexadecimal remainder; zero is "0".
func (m Modes) String() string {
	if m == 0 {
		return "0"
	}
	var parts []string
	for i, name := range modeNames {
		if m&(1<<i) != 0 {
			parts = append(parts, name)
		}
	}
	if rest := m &^ (1<<len(modeNames) - 1); rest != 0 {
		parts = append(parts, "0x"+strconv.FormatUint(uint64(rest), 16))
	}
	return strings.Join(parts, "|")
}
