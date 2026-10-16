package wire

import "github.com/Akkanbe/gotwamp"

// Modes is the canonical public value.
type Modes = gotwamp.Modes

// Aliases of the canonical public constants.
//
//declscope:ignore overexported // the complete IANA mode registry is retained for protocol decoders and tests
const (
	ModeUnauthenticated          = gotwamp.ModeUnauthenticated
	ModeAuthenticated            = gotwamp.ModeAuthenticated
	ModeEncrypted                = gotwamp.ModeEncrypted
	ModeMixed                    = gotwamp.ModeMixed
	ModeIndividualSessionControl = gotwamp.ModeIndividualSessionControl
	ModeReflectOctets            = gotwamp.ModeReflectOctets
	ModeSymmetricalSize          = gotwamp.ModeSymmetricalSize
	ModeIKEv2Derived             = gotwamp.ModeIKEv2Derived
	ModeDSCPECNMonitoring        = gotwamp.ModeDSCPECNMonitoring
	ModePTPv2Timestamp           = gotwamp.ModePTPv2Timestamp
	SecurityModes                = gotwamp.SecurityModes
)
