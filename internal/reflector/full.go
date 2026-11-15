package reflector

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// VerifySender selects how a Full session checks the source of test packets
// (SPEC §6.4.1, CMP-09).
type VerifySender uint8

const (
	// VerifyAddress accepts packets from the Sender Address, the default.
	VerifyAddress VerifySender = iota
	// VerifyAddressPort also requires the Sender Port.
	VerifyAddressPort
	// VerifyNone accepts the first source seen and then only that one,
	// for senders behind PAT.
	VerifyNone
)

// FullConfig configures the Full Session-Reflector. Zero values take the
// defaults of Config.
type FullConfig struct {
	// VerifySender selects the source check.
	VerifySender VerifySender
	// TTL is the TTL or Hop Limit of replies (1–255).
	TTL int
	// MaxPacketSize drops longer UDP payloads.
	MaxPacketSize int
	// Timestamping selects how receive timestamps are taken.
	Timestamping tstamp.Mode
	// ReadBuffer is SO_RCVBUF; 0 leaves the tstamp default.
	ReadBuffer int
	// UserspaceError and KernelError are added to the clock error in the
	// Error Estimate (SPEC §8.4).
	UserspaceError time.Duration
	KernelError    time.Duration
	// EnforceStartTime drops the packets that arrive before the Start Time of
	// the request (reflector.control.enforce_start_time, like SR Linux's
	// enforce-test-session-start-time). By default a session reflects from
	// Start-Sessions on whatever its Start Time: the Start Time is on the
	// sender's clock, which may run ahead of the reflector's by the clock
	// error of either host (perfSONAR's twping adds the kernel's pending NTP
	// offset, milliseconds), so a sender that starts at its Start Time would
	// lose its first packets. A sender cannot send before Start-Ack anyway.
	EnforceStartTime bool
}

// WithDefaults returns c with zero values replaced by the defaults.
func (c FullConfig) WithDefaults() FullConfig {
	if c.TTL == 0 {
		c.TTL = defaultTTL
	}
	if c.MaxPacketSize == 0 {
		c.MaxPacketSize = defaultMaxPacketSize
	}
	if c.UserspaceError == 0 {
		c.UserspaceError = DefaultUserspaceError
	}
	if c.KernelError == 0 {
		c.KernelError = defaultKernelError
	}
	return c
}

// Validate checks a configuration that already has its defaults.
func (c FullConfig) Validate() error {
	var errs []error
	if c.VerifySender > VerifyNone {
		errs = append(errs, fmt.Errorf("unknown verify_sender %d", c.VerifySender))
	}
	if c.TTL < 1 || c.TTL > 255 {
		errs = append(errs, fmt.Errorf("ttl %d is out of range 1-255", c.TTL))
	}
	if c.MaxPacketSize < wire.SenderHeaderSize || c.MaxPacketSize > maxUDPPayload {
		errs = append(errs, fmt.Errorf("max packet size %d is out of range 14-%d", c.MaxPacketSize, maxUDPPayload))
	}
	if c.UserspaceError < 0 || c.KernelError < 0 {
		errs = append(errs, errors.New("timestamping error is negative"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("full reflector config: %w", err)
	}
	return nil
}

// Full is the Session-Reflector of TWAMP Full. It implements
// server.SessionHandler: the control server opens one socket per accepted
// session through it.
type Full struct {
	cfg      FullConfig
	listen   func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error)
	clock    ClockState
	log      *slog.Logger
	counters counters
}

var _ server.SessionHandler = (*Full)(nil)

// NewFull validates cfg, applying its defaults. deps.Listen opens the
// session sockets; nil means tstamp.Listen.
func NewFull(cfg FullConfig, deps Deps) (*Full, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	f := &Full{cfg: cfg, listen: deps.Listen, clock: deps.Clock, log: deps.Logger}
	if f.listen == nil {
		f.listen = tstamp.Listen
	}
	if f.log == nil {
		f.log = slog.New(slog.DiscardHandler)
	}
	return f, nil
}

// Counters returns the packet counters of every Full session together.
func (f *Full) Counters() Counters { return f.counters.snapshot() }

// Open binds the socket of a session and starts reading it. Packets are
// dropped until Start (SPEC §5.7). Bind errors are returned as is, so the
// server can tell a port in use from an address that is not local.
func (f *Full) Open(spec server.SessionSpec) (server.Session, error) {
	mode := spec.Security.TestMode()
	if mode == 0 {
		mode = wire.SecurityUnauthenticated
	}
	var sealer *replySealer
	if mode != wire.SecurityUnauthenticated {
		crypto, err := wire.NewTestCrypto(mode, spec.TestKeys)
		if err != nil {
			return nil, fmt.Errorf("full reflector: %w: %w", err, server.ErrUnsupportedRequest)
		}
		sealer = &replySealer{crypto: crypto}
	}
	// A Session-Sender packet is its header (14 octets, 48 when
	// authenticated, both grown by the MBZ octets of Symmetrical Size) plus
	// the padding; one longer than max_packet_size would be dropped, so the
	// session could only lose every packet (audit W5).
	header := spec.Layout.SenderHeaderSize(mode)
	if size := int64(header) + int64(spec.PaddingLength); size > int64(f.cfg.MaxPacketSize) {
		return nil, fmt.Errorf("full reflector: padding %d makes %d-octet packets, above max_packet_size %d: %w",
			spec.PaddingLength, size, f.cfg.MaxPacketSize, server.ErrUnsupportedRequest)
	}
	local := netip.AddrPortFrom(spec.Local.Addr().Unmap(), spec.Local.Port())
	network := "udp6"
	if local.Addr().Is4() {
		network = "udp4"
	}
	opts := tstamp.Options{
		TTL:        f.cfg.TTL,
		TOS:        int(spec.DSCP&0x3F) << 2, // RFC 5357 §3.5: replies carry the Type-P DSCP
		Mode:       f.cfg.Timestamping,
		ReadBuffer: f.cfg.ReadBuffer,
	}
	conn, err := f.listen(network, local, opts)
	if err != nil {
		return nil, fmt.Errorf("full reflector: listen %v: %w", local, err)
	}
	s := &fullSession{
		f: f, spec: spec, conn: conn, done: make(chan struct{}), header: header, sealer: sealer,
		format: replyFormat{dscpecn: spec.DSCPECNMonitoring, symmetric: spec.Layout.SymmetricalSize},
	}
	go s.serve()
	return s, nil
}

// fullSession is the reflector side of one Full test session.
type fullSession struct {
	f    *Full
	spec server.SessionSpec
	conn tstamp.Conn
	done chan struct{}
	// header is the least length of a Session-Sender packet: 14, or 48 in
	// the authenticated and encrypted modes, 27 or 64 more under
	// Symmetrical Size.
	header int
	// sealer protects the packets of those modes; nil when unauthenticated.
	sealer *replySealer
	// format is the layout of the replies.
	format replyFormat

	mu      sync.Mutex
	startAt time.Time      // zero until Start
	stopAt  time.Time      // zero until Stop
	locked  netip.AddrPort // the source VerifyNone fixed on

	received  atomic.Uint64
	reflected atomic.Uint64
	last      atomic.Int64 // UnixNano of the last accepted packet, 0 if none
}

// Start reflects packets that arrive from at, or with EnforceStartTime from
// max(at, StartTime).
func (s *fullSession) Start(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f.cfg.EnforceStartTime && s.spec.StartTime.After(at) {
		at = s.spec.StartTime
	}
	s.startAt = at
}

// Stop reflects only packets that arrive before until.
func (s *fullSession) Stop(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopAt = until
}

// Close closes the socket and waits for the reader to return.
func (s *fullSession) Close() error {
	err := s.conn.Close()
	<-s.done
	return err
}

// Stats returns the packet counts.
func (s *fullSession) Stats() server.SessionStats {
	st := server.SessionStats{Received: s.received.Load(), Reflected: s.reflected.Load()}
	if ns := s.last.Load(); ns != 0 {
		st.LastReceived = time.Unix(0, ns)
	}
	return st
}

func (s *fullSession) serve() {
	defer close(s.done)
	buf := make([]byte, max(s.f.cfg.MaxPacketSize+1, wire.AuthReflectorHeaderSize))
	est := estimator{clock: s.f.clock, methodError: reflectorMethodError(s.f.cfg.UserspaceError, s.f.cfg.KernelError)}
	errs := readErrors{log: s.f.log}
	var seq uint32
	for {
		n, from, rx, err := s.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				errs.failed("full reflector: read failed", err, "sid", s.spec.SID, "local", s.conn.LocalAddr())
			}
			continue
		}
		if rx.At.IsZero() {
			rx.At = time.Now()
		}
		// A packet that fails its HMAC is not the Session-Sender's: it must
		// neither count for REFWAIT nor fix the source of VerifyNone
		// (RFC 4656 §4.2: it MUST be discarded).
		if s.sealer != nil && (n < s.header || n > s.f.cfg.MaxPacketSize || s.sealer.crypto.OpenSender(buf[:n]) != nil) {
			s.f.counters.received.Add(1)
			s.f.counters.dropped[DropMalformed].Add(1)
			continue
		}
		if reason, ok := s.admit(n, from, rx.At); !ok {
			s.f.counters.dropped[reason].Add(1)
			continue
		}
		s.received.Add(1)
		s.last.Store(rx.At.UnixNano())
		// The reply cannot fail: the length was checked and buf holds the
		// reply header.
		var m int
		if s.sealer != nil {
			m, _ = buildAuthReply(buf, n, seq, est.estimate(rx.At), rx, s.format)
		} else {
			m, _ = buildReply(buf, n, seq, est.estimate(rx.At), rx, s.format)
		}
		seq++
		// The session socket is bound to one address: no source to set.
		if err := sendReply(s.conn, buf[:m], from, netip.Addr{}, -1, s.sealer); err != nil {
			s.f.counters.sendErrors.Add(1)
			s.f.log.Debug("full reflector: send failed", "sid", s.spec.SID, "to", from, "err", err)
			continue
		}
		s.reflected.Add(1)
		s.f.counters.reflected.Add(1)
	}
}

// admit counts a datagram and decides whether to reflect it: the source must
// match (SPEC §6.4.1), the session must have started and not drained, and the
// length must be valid.
func (s *fullSession) admit(n int, from netip.AddrPort, at time.Time) (DropReason, bool) {
	s.f.counters.received.Add(1)
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.f.cfg.VerifySender {
	case VerifyAddress:
		if from.Addr() != s.spec.Sender.Addr().Unmap() {
			return DropUnknownSession, false
		}
	case VerifyAddressPort:
		if from.Addr() != s.spec.Sender.Addr().Unmap() || from.Port() != s.spec.Sender.Port() {
			return DropUnknownSession, false
		}
	case VerifyNone:
		if s.locked.IsValid() && from != s.locked {
			return DropUnknownSession, false
		}
	}
	switch {
	case s.startAt.IsZero() || at.Before(s.startAt):
		return DropNotStarted, false
	case !s.stopAt.IsZero() && at.After(s.stopAt):
		return DropDrained, false
	case n < s.header || n > s.f.cfg.MaxPacketSize:
		return DropMalformed, false
	}
	if s.f.cfg.VerifySender == VerifyNone && !s.locked.IsValid() {
		s.locked = from
	}
	return 0, true
}

// buildAuthReply turns the opened plaintext of an authenticated or encrypted
// Session-Sender packet in b[:n] into the reply plaintext in place, like
// buildReply, and returns its length, max(n, 112). sendReply seals it.
func buildAuthReply(b []byte, n int, seq uint32, ee errest.Estimate, rx tstamp.RxMeta, f replyFormat) (int, error) {
	p := wire.AuthReflectorPacket{
		Seq:              seq,
		ErrorEstimate:    ee,
		ReceiveTimestamp: ntptime.FromTime(rx.At),
		SenderTTL:        senderTTL(rx),
		HasDSCPECN:       f.dscpecn,
		SymmetricalSize:  f.symmetric,
	}
	if f.dscpecn && rx.TOS >= 0 && rx.TOS <= 255 {
		p.DSCPECN = wire.DSCPECN(rx.TOS)
	}
	return p.Reflect(b, n)
}
