package client

import "github.com/Akkanbe/gotwamp"

// Stage is the canonical public value.
type Stage = gotwamp.Stage

// Error is the public control protocol failure.
type Error = gotwamp.ControlError

// Aliases of the canonical public constants.
//
//declscope:ignore overexported // the complete public control error vocabulary is retained for internal protocol callers and tests
const (
	StageConnect  = gotwamp.StageConnect
	StageGreeting = gotwamp.StageGreeting
	StageSetup    = gotwamp.StageSetup
	StageRequest  = gotwamp.StageRequest
	StageStart    = gotwamp.StageStart
	StageTest     = gotwamp.StageTest
	StageStop     = gotwamp.StageStop
)

// Aliases of the canonical public error sentinels.
//
//declscope:ignore overexported // the complete public control error vocabulary is retained for internal protocol callers and tests
var (
	ErrServerRefused     = gotwamp.ErrServerRefused
	ErrNoCommonMode      = gotwamp.ErrNoCommonMode
	ErrCountTooLarge     = gotwamp.ErrCountTooLarge
	ErrCountTooSmall     = gotwamp.ErrCountTooSmall
	ErrRejected          = gotwamp.ErrRejected
	ErrAlternatePort     = gotwamp.ErrAlternatePort
	ErrControlLost       = gotwamp.ErrControlLost
	ErrState             = gotwamp.ErrState
	ErrInvalidPort       = gotwamp.ErrInvalidPort
	ErrReflectedOctets   = gotwamp.ErrReflectedOctets
	ErrUnexpectedMessage = gotwamp.ErrUnexpectedMessage
	ErrTimeout           = gotwamp.ErrTimeout
)
