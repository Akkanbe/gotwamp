// Package sender runs the TWAMP Session-Sender (RFC 5357 §4.1, SPEC §6.5):
// it sends test packets on an absolute schedule, matches the reflected
// packets, and streams per-packet results and run or window summaries.
//
// The same Sender serves TWAMP Light, where it opens its own socket, and
// TWAMP Full, where the control layer binds the Sender Port before
// Request-TW-Session and hands the socket over with [WithConn].
package sender

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Defaults of SPEC Appendix A and §9.2.
const (
	DefaultTimeout    = stats.DefaultTimeout
	DefaultPadding    = wire.DefaultPaddingLength
	defaultWindow     = time.Minute
	defaultSpinBefore = 200 * time.Microsecond
)

// Defaults the run reads too (SPEC Appendix A, §8.4).
//
//declscope:package // DefaultConfig and normalize set them; the run falls back on the errors, and its tests on the interval
const (
	defaultInterval       = 100 * time.Millisecond
	defaultUserspaceError = 50 * time.Microsecond
	defaultKernelError    = 5 * time.Microsecond
)

// maxPadding is the largest padding a Sender accepts, the reflector's
// default max_padding.
const maxPadding = 9000

// PaddingFill selects the content of the Packet Padding.
type PaddingFill uint8

const (
	// PaddingAuto is random for TWAMP Full and zero for TWAMP Light (SPEC
	// §6.2): some Light reflectors read the padding as STAMP TLVs.
	PaddingAuto PaddingFill = iota
	// PaddingRandom fills the padding with pseudo-random octets.
	PaddingRandom
	// PaddingZero fills the padding with zeros.
	PaddingZero
)

// Config describes one test session. Zero durations select the defaults.
type Config struct {
	// Target is the reflector address and its Receiver Port. Only replies
	// from this address and port are accepted.
	Target netip.AddrPort
	// Source is the local address and port to bind. The zero value lets the
	// system choose both. It is not used with WithConn.
	Source netip.AddrPort
	// Count is the number of packets of the run. Zero runs continuously,
	// summarizing every Window, until the context is canceled.
	Count uint32
	// Window is the summary period of a continuous session. Each window
	// holds Window / Interval packets, at least one.
	Window time.Duration
	// Interval is the time between the scheduled sends.
	Interval time.Duration
	// Timeout is how long after T1 a reply is accepted.
	Timeout time.Duration
	// Padding is the length of the Packet Padding in octets, taken as given:
	// zero sends 14-octet packets. DefaultPadding is the usual choice.
	Padding int
	// PaddingFill selects the padding content.
	PaddingFill PaddingFill
	// DSCP is the DSCP of the test packets, 0–63. ECN is always Not-ECT.
	// It is not used with WithConn.
	DSCP int
	// TTL is the IP TTL / Hop Limit of the test packets, 1–255. Zero means
	// 255. With WithConn it must match the socket, since hop counts derive
	// from it.
	TTL int
	// SpinBefore is how long before each scheduled send the Sender stops
	// sleeping and spins. Zero means 200 µs; negative disables
	// spinning.
	SpinBefore time.Duration
	// Timestamping is the timestamping mode of the socket. It is not used
	// with WithConn.
	Timestamping tstamp.Mode
	// ReadBuffer is the receive socket buffer in octets; zero means the
	// tstamp default. It is not used with WithConn.
	ReadBuffer int
	// UserspaceError, KernelError and HardwareError are the errors of the
	// timestamping methods added to the clock error in the Error Estimate
	// (SPEC §8.4); the socket's mode picks one. Zero means 50 µs and 5 µs
	// (SPEC appendix A), and KernelError for HardwareError: a card's
	// timestamps are at least as good as the kernel's.
	UserspaceError time.Duration
	KernelError    time.Duration
	HardwareError  time.Duration
	// MethodError, when non-zero, overrides UserspaceError and KernelError
	// whatever the mode.
	MethodError time.Duration
	// StatelessReflector is set when the reflector copies the Sender
	// Sequence Number (SPEC §6.6 copy_sender). It is ignored by RunFull.
	StatelessReflector bool
	// DropZeroMultiplier discards replies whose Error Estimate has
	// Multiplier 0 instead of accepting them with invalid one-way delays
	// (tolerate_zero_multiplier: false, SPEC §7.6).
	DropZeroMultiplier bool
	// MaxSamples bounds the values each delay metric of a run or window
	// keeps for percentiles. Zero means 100,000 (the stats default).
	MaxSamples int
	// DSCPECNMonitoring reads the S-DSCP-ECN octet at offset 41 of the
	// replies (RFC 7750): the DSCP and ECN with which the test packets
	// reached the reflector. The reflector must write it: a Light reflector
	// configured for it, or a Full session that negotiated Modes bit 8.
	// Replies shorter than 44 octets are then malformed.
	DSCPECNMonitoring bool
	// STAMP sends STAMP Session-Sender packets (RFC 8762 §4.2.1, TWAMP Light
	// only): the 44-octet base with SSID, and the Padding, when not zero,
	// as one Extra Padding TLV (RFC 8972 §4.2), which needs at least its
	// 4-octet header. Replies are read as TWAMP replies, which they are on
	// the wire; one carrying another non-zero SSID counts as foreign.
	STAMP bool
	// SSID is the STAMP Session-Sender Identifier (RFC 8972 §3); 0 sends
	// none.
	SSID uint16
	// LockOSThread pins the send and receive goroutines to OS threads
	// (realtime.lock_os_thread, SPEC §8.6).
	LockOSThread bool
	// TxTimestampWait is how long a reply waits for the kernel transmit
	// timestamp of its packet (tx_timestamp_wait, SPEC §8.2). Zero means
	// 1 ms (SPEC appendix A); negative does not wait.
	TxTimestampWait time.Duration
}

// DefaultConfig returns the configuration of SPEC Appendix A for target.
func DefaultConfig(target netip.AddrPort) Config {
	return Config{
		Target:   target,
		Count:    100,
		Window:   defaultWindow,
		Interval: defaultInterval,
		Timeout:  DefaultTimeout,
		Padding:  DefaultPadding,
		TTL:      255,
	}
}

// Hooks receive the results of a run. Every hook is optional. They are
// called from the goroutine that runs Run, one at a time, and must return
// quickly: replies wait in the socket buffer meanwhile, and a hook that
// blocks holds the run, its cancellation included, until it returns. A
// consumer that may block, such as a network stream, must queue the
// results and drop or fail when the queue is full.
type Hooks struct {
	// Packet is called for every reply from the reflector, duplicates and
	// strays included.
	Packet func(PacketResult)
	// Lost is called when a packet times out.
	Lost func(LostPacket)
	// Summary is called when a run or window completes or is aborted.
	Summary func(Summary)
}

// Option adapts a Sender to TWAMP Full, or injects its dependencies.
type Option func(*options)

type options struct {
	conn     tstamp.Conn
	full     bool
	security testSecurity
	layout   testLayout
	rand     io.Reader
	clock    func() (synced bool, maxErr time.Duration)
	hooks    Hooks
}

// WithConn makes the Sender use conn, already bound and configured with the
// TTL and TOS of the session, instead of opening its own socket. TWAMP Full
// binds the Sender Port before Request-TW-Session this way. The Sender
// never closes conn.
func WithConn(conn tstamp.Conn) Option {
	return func(o *options) { o.conn = conn }
}

// withFull marks a session negotiated with TWAMP-Control: PaddingAuto is
// random, and the reflector numbers its packets per session.
//
//declscope:package // RunFull sets it; the run tests set it to play a Full session
func withFull() Option {
	return func(o *options) { o.full = true }
}

// testSecurity is the protection of the test packets of a TWAMP Full
// session in the authenticated or encrypted mode (RFC 4656 §4.1.2); the
// zero value leaves them unauthenticated.
//
//declscope:package // RunFull sets it from Accept-Session; the run applies it
type testSecurity struct {
	mode wire.SecurityMode
	keys wire.TestKeys
}

// withSecurity sends and receives the test packets in mode, which takes
// effect only when authenticated or encrypted, with the session's keys.
//
//declscope:package // RunFull passes the mode it negotiated
func withSecurity(mode wire.SecurityMode, keys wire.TestKeys) Option {
	return func(o *options) {
		if mode := mode.TestMode(); mode == wire.SecurityAuthenticated || mode == wire.SecurityEncrypted {
			o.security = testSecurity{mode: mode, keys: keys}
		}
	}
}

// testMode returns the security mode of the test packets.
func (t testSecurity) testMode() wire.SecurityMode {
	if t.mode == 0 {
		return wire.SecurityUnauthenticated
	}
	return t.mode
}

// testLayout is the RFC 6038 layout of the test packets of a TWAMP Full
// session.
//
//declscope:package // RunFull sets it from Accept-Session; the run checks the reflected octets
type testLayout struct {
	layout       wire.PaddingLayout
	serverOctets uint16
}

// withLayout sends the test packets in the RFC 6038 layout of the session:
// the MBZ octets of Symmetrical Size, and the server octets at the start of
// the padding to be reflected, whose return each reply is checked for.
//
//declscope:package // RunFull passes what it negotiated
func withLayout(layout wire.PaddingLayout, serverOctets uint16) Option {
	return func(o *options) { o.layout = testLayout{layout: layout, serverOctets: serverOctets} }
}

// withRand sets the source of random padding. The default is ChaCha8
// seeded from crypto/rand.
//
//declscope:package // the padding tests make the random padding predictable
func withRand(r io.Reader) Option {
	return func(o *options) { o.rand = r }
}

// WithClock sets the clock state written into the Error Estimate: whether
// the clock is synchronized and its maximum error. Without it the Sender
// sends the unknown estimate (S clear, Scale 63, Multiplier 255).
func WithClock(status func() (synced bool, maxErr time.Duration)) Option {
	return func(o *options) { o.clock = status }
}

// WithHooks sets the receivers of results.
func WithHooks(h Hooks) Option {
	return func(o *options) { o.hooks = h }
}

// Sender is one Session-Sender. Create it with New, call Run once, then
// Close.
type Sender struct {
	cfg     Config
	opts    options
	conn    tstamp.Conn
	ownConn bool
	// packet is the Session-Sender packet; its padding is filled once and
	// the header is rewritten for every send.
	packet    []byte
	used      bool
	closeOnce sync.Once
	closeErr  error
}

// New validates cfg and opens the socket, unless one is given with
// WithConn.
func New(cfg Config, opts ...Option) (*Sender, error) {
	s := &Sender{}
	for _, o := range opts {
		o(&s.opts)
	}
	if err := normalize(&cfg, s.opts.full); err != nil {
		return nil, err
	}
	s.cfg = cfg
	if err := s.fillPadding(); err != nil {
		return nil, err
	}

	if s.opts.conn != nil {
		s.conn = s.opts.conn
		return s, nil
	}
	network := "udp6"
	if cfg.Target.Addr().Is4() {
		network = "udp4"
	}
	conn, err := tstamp.Listen(network, cfg.Source, tstamp.Options{
		TTL:          cfg.TTL,
		TOS:          cfg.DSCP << 2,
		Mode:         cfg.Timestamping,
		ReadBuffer:   cfg.ReadBuffer,
		TxTimestamps: cfg.Timestamping != tstamp.ModeUserspace,
	})
	if err != nil {
		return nil, fmt.Errorf("sender: open socket: %w", err)
	}
	s.conn, s.ownConn = conn, true
	return s, nil
}

// fillPadding allocates the Session-Sender packet and fills its padding,
// once for the session.
func (s *Sender) fillPadding() error {
	hdr := s.header()
	s.packet = make([]byte, hdr+s.cfg.Padding)
	defer s.finishPadding(hdr)
	fill := s.cfg.PaddingFill
	if fill != PaddingRandom && (fill != PaddingAuto || !s.opts.full) || s.cfg.Padding == 0 {
		return nil
	}
	r := s.opts.rand
	if r == nil {
		var seed [32]byte
		_, _ = rand.Read(seed[:])
		r = mrand.NewChaCha8(seed)
	}
	if _, err := io.ReadFull(r, s.packet[hdr:]); err != nil {
		return fmt.Errorf("sender: fill padding: %w", err)
	}
	return nil
}

// header returns the length of the Session-Sender header: 14, or 48 in the
// authenticated and encrypted modes, grown by the MBZ octets of Symmetrical
// Size, or the 44-octet STAMP base.
func (s *Sender) header() int {
	if s.cfg.STAMP {
		return wire.STAMPBaseSize
	}
	return s.opts.layout.layout.SenderHeaderSize(s.opts.security.testMode())
}

// finishPadding writes into the filled padding what goes at its start: the
// server octets of RFC 6038, or the header of the STAMP Extra Padding TLV.
func (s *Sender) finishPadding(hdr int) {
	padding := s.packet[hdr:]
	if l := s.opts.layout; l.serverOctets != 0 && l.layout.ReflectLength >= 2 && len(padding) >= 2 {
		binary.BigEndian.PutUint16(padding, l.serverOctets)
	}
	if s.cfg.STAMP && len(padding) > 0 {
		_ = wire.PutExtraPadding(padding) // normalize checked the length
	}
}

// WithDefaults returns a normalized Light configuration. Validate reports any
// invalid fields; this method performs no I/O.
func (cfg Config) WithDefaults() Config {
	// Defaults are independent of endpoint validation.
	applyDefaults(&cfg)
	return cfg
}

// Validate checks a Light configuration without opening a socket.
func (cfg Config) Validate() error { return normalize(&cfg, false) }

// RunLight opens a sender, runs it once, and releases its socket.
func RunLight(ctx context.Context, cfg Config, opts ...Option) (Summary, error) {
	s, err := New(cfg, opts...)
	if err != nil {
		return Summary{}, err
	}
	summary, runErr := s.Run(ctx)
	return summary, errors.Join(runErr, s.Close())
}

func applyDefaults(cfg *Config) {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Window <= 0 {
		cfg.Window = defaultWindow
	}
	if cfg.SpinBefore == 0 {
		cfg.SpinBefore = defaultSpinBefore
	}
	if cfg.TTL == 0 {
		cfg.TTL = 255
	}
}

// normalize applies the defaults to cfg and validates it.
func normalize(cfg *Config, full bool) error {
	if !cfg.Target.IsValid() || cfg.Target.Port() == 0 {
		return errors.New("sender: target address and port are required")
	}
	cfg.Target = netip.AddrPortFrom(cfg.Target.Addr().Unmap(), cfg.Target.Port())
	if cfg.Source.IsValid() && cfg.Source.Addr().Is4() != cfg.Target.Addr().Is4() {
		return fmt.Errorf("sender: source %v and target %v differ in address family", cfg.Source, cfg.Target)
	}
	applyDefaults(cfg)
	if full {
		cfg.StatelessReflector = false
	}
	switch {
	case cfg.DSCPECNMonitoring && full:
		return errors.New("sender: dscp_ecn_monitoring is supported in light mode only")
	case cfg.STAMP && full:
		return errors.New("sender: stamp is for TWAMP Light; TWAMP Full negotiates its packets")
	case cfg.STAMP && cfg.DSCPECNMonitoring:
		return errors.New("sender: stamp and dscp_ecn_monitoring exclude each other: STAMP keeps offset 41 MBZ")
	case cfg.STAMP && cfg.Padding > 0 && cfg.Padding < 4:
		return fmt.Errorf("sender: stamp padding %d is shorter than the 4-octet Extra Padding TLV header", cfg.Padding)
	case cfg.SSID != 0 && !cfg.STAMP:
		return errors.New("sender: an SSID needs stamp")
	case cfg.Padding < 0 || cfg.Padding > maxPadding:
		return fmt.Errorf("sender: padding %d out of range 0-%d", cfg.Padding, maxPadding)
	case cfg.DSCP < 0 || cfg.DSCP > 63:
		return fmt.Errorf("sender: dscp %d out of range 0-63", cfg.DSCP)
	case cfg.TTL < 1 || cfg.TTL > 255:
		return fmt.Errorf("sender: ttl %d out of range 1-255", cfg.TTL)
	case cfg.PaddingFill > PaddingZero:
		return fmt.Errorf("sender: unknown padding fill %d", cfg.PaddingFill)
	}
	if cfg.UserspaceError < 0 || cfg.KernelError < 0 || cfg.HardwareError < 0 || cfg.MethodError < 0 {
		return errors.New("sender: timestamping error must not be negative")
	}
	if cfg.MaxSamples < 0 {
		return errors.New("sender: max samples must not be negative")
	}
	return (tstamp.Options{TTL: cfg.TTL, TOS: cfg.DSCP << 2, Mode: cfg.Timestamping, ReadBuffer: cfg.ReadBuffer}).Validate()
}

// Run sends the packets and collects their replies, until every packet of a
// run with a Count is decided or the context is canceled. It returns the
// summary of the run, or of the last window of a continuous session.
//
// Canceling ctx aborts the run: the windows still open are summarized with
// Aborted set, their undecided packets counted as Pending, and Run returns
// the context's error with the last summary. Any other error is a failure of
// the socket. Run may be called once.
func (s *Sender) Run(ctx context.Context) (Summary, error) {
	if s.used {
		return Summary{}, errors.New("sender: Run called twice")
	}
	s.used = true
	p := runParams{
		cfg: s.cfg, conn: s.conn, packet: s.packet, clock: s.opts.clock, hooks: s.opts.hooks,
		fresh: s.opts.full, sec: s.opts.security,
	}
	if l := s.opts.layout.layout.ReflectLength; l > 0 {
		hdr := s.header()
		p.reflected = s.packet[hdr:min(hdr+int(l), len(s.packet))]
	}
	return runSession(ctx, p)
}

// LocalAddr returns the address and port the Sender sends from.
func (s *Sender) LocalAddr() netip.AddrPort {
	return s.conn.LocalAddr()
}

// Close closes the socket the Sender opened. A socket given with WithConn
// is left open.
func (s *Sender) Close() error {
	s.closeOnce.Do(func() {
		if s.ownConn {
			if err := s.conn.Close(); err != nil {
				s.closeErr = fmt.Errorf("sender: close: %w", err)
			}
		}
	})
	return s.closeErr
}
