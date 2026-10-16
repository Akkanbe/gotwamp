// Package sender measures paths with TWAMP Light or negotiated Full sessions.
// Sockets require Linux. No daemon, scheduler, storage or exporter is started.
package sender

import (
	"context"
	"net/netip"
	"time"

	engine "github.com/Akkanbe/gotwamp/internal/sender"
)

// Config describes one bounded or continuous test session. Zero Count means
// continuous operation, not a default packet count. Use DefaultConfig for a
// bounded run. Validate and WithDefaults perform no I/O.
type Config = engine.Config

// FullConfig configures control negotiation for one Full session. Target in
// Config is ignored. Security modes and optional extensions are explicit.
type FullConfig = engine.FullConfig

// PortRange is an inclusive range of UDP sender ports.
type PortRange = engine.PortRange

// PaddingFill selects the content of packet padding.
type PaddingFill = engine.PaddingFill

// Packet padding policies.
const (
	PaddingAuto   = engine.PaddingAuto
	PaddingRandom = engine.PaddingRandom
	PaddingZero   = engine.PaddingZero
)

// DefaultTimeout and DefaultPadding are the normal test timeout and padding.
const (
	DefaultTimeout = engine.DefaultTimeout
	DefaultPadding = engine.DefaultPadding
)

// Hooks receives synchronous packet, timeout and summary events. Hooks must
// return quickly; a blocked hook also blocks cancellation. A consumer that may
// block must provide its own bounded queue and overflow policy.
type Hooks = engine.Hooks

// PacketResult describes a reply, including classification and clock validity.
type PacketResult = engine.PacketResult

// LostPacket identifies a packet whose timeout expired without a reply.
type LostPacket = engine.LostPacket

// Summary describes a bounded run or one continuous window. Inspect Invalid,
// Aborted, Pending, distribution Counts, OWDValid and LossSplitValid before
// using measurements. Zero values alone do not establish availability.
type Summary = engine.Summary

// Option configures event delivery or the local clock state.
type Option = engine.Option

// Sender owns one Light socket. Call Run once, then Close. Cancellation joins
// the run's workers. Close is idempotent and interrupts a running socket;
// wait for Run to return before discarding its results.
type Sender = engine.Sender

// DefaultConfig returns the usual bounded session for target (100 packets).
func DefaultConfig(target netip.AddrPort) Config { return engine.DefaultConfig(target) }

// WithHooks delivers events from the goroutine running the measurement.
func WithHooks(h Hooks) Option { return engine.WithHooks(h) }

// WithClock supplies synchronization state and maximum clock error. It must
// return quickly and be safe for the caller's usage. Without it the clock
// estimate is unknown and one-way delay is invalid.
func WithClock(state func() (synced bool, maxErr time.Duration)) Option {
	return engine.WithClock(state)
}

// New validates a Light session and acquires its socket without sending.
func New(cfg Config, opts ...Option) (*Sender, error) { return engine.New(cfg, opts...) }

// RunLight acquires a Light socket, runs the measurement and releases it.
// Cancellation returns the context error with an aborted summary.
func RunLight(ctx context.Context, cfg Config, opts ...Option) (Summary, error) {
	return engine.RunLight(ctx, cfg, opts...)
}

// ValidateFull checks test, control, security and socket configuration without
// network I/O. No peer connection is required to validate configuration.
func ValidateFull(cfg Config, full FullConfig) error { return engine.ValidateFull(cfg, full) }

// RunFull negotiates and measures one session on its own control connection.
// It supports bounded runs and continuous windows. Errors of control exchanges
// are *gotwamp.ControlError; errors.Is matches the public control sentinels.
// Cancellation aborts open measurement windows and attempts Stop-Sessions for
// at most two additional seconds. Cancellation during teardown preserves
// completed packet counts and abort/pending state. Bounded and aborted-window
// summary hooks run after teardown; completed continuous windows stream immediately.
func RunFull(ctx context.Context, cfg Config, full FullConfig, opts ...Option) (Summary, error) {
	return engine.RunFull(ctx, cfg, full, opts...)
}
