//declscope:namespace server

// Package server implements the TWAMP-Control Server (RFC 5357 §3,
// SPEC §5.7) in the unauthenticated, authenticated, encrypted and mixed
// modes (RFC 4656 §3.1, RFC 5618).
//
// The server runs one goroutine per control connection. It validates
// requests, allocates Receiver Ports, enforces the connection and session
// limits, SERVWAIT and REFWAIT, and drives the test sessions through a
// SessionHandler, which owns the reflector sockets.
package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Defaults of Config (SPEC §9.2, §9.9, appendix A).
const (
	defaultMaxConnections           = 64
	defaultMaxSessionsPerConnection = 16
	defaultMaxSessions              = 1024
	defaultServWait                 = 900 * time.Second
	defaultRefWait                  = 900 * time.Second
	defaultPartialMessageTimeout    = 60 * time.Second
	defaultMaxPadding               = 9000
)

// Config configures a Server. Zero values take the defaults.
type Config struct {
	// ACL lists the client prefixes allowed; empty allows every client.
	// Others get a Server Greeting with Modes 0.
	ACL []netip.Prefix
	// MaxConnections caps concurrent control connections.
	MaxConnections int
	// MaxSessionsPerConnection caps the sessions of one connection (Accept 4).
	MaxSessionsPerConnection int
	// MaxSessions caps the sessions of the server (Accept 5).
	MaxSessions int
	// ServWait closes a connection idle this long outside test sessions.
	ServWait time.Duration
	// RefWait releases a started session that receives nothing this long.
	RefWait time.Duration
	// PartialMessageTimeout closes a connection that leaves a message
	// unfinished this long; it also bounds every write.
	PartialMessageTimeout time.Duration
	// TestPorts is where alternate Receiver Ports come from.
	TestPorts PortRange
	// IgnoreRequestedPort always picks from TestPorts
	// (honor_requested_port: false).
	IgnoreRequestedPort bool
	// MaxPadding is the largest Padding Length accepted (Accept 3 above it).
	MaxPadding uint32
	// Count is the PBKDF2 count of the Server Greeting.
	Count uint32
	// Rand supplies Challenge, Salt, Server-IV, SIDs and port choices; nil
	// means crypto/rand.
	Rand io.Reader
	// Now returns the current time for the protocol (Start-Time, SIDs,
	// SERVWAIT and REFWAIT); nil means time.Now. Socket deadlines always
	// follow the real clock.
	Now func() time.Time
	// StartTime is when this server instance started, sent in Server-Start;
	// zero means when New is called.
	StartTime time.Time
	// Modes are the modes offered in the Server Greeting: any of the
	// security modes unauthenticated, authenticated, encrypted and mixed
	// (Modes bits 0–3), with any of FeatureModes; zero offers
	// unauthenticated mode only.
	Modes wire.Modes
	// Keys holds the shared secrets of the authenticated, encrypted and
	// mixed modes, by the identity of the KeyID. Required when Modes offers
	// one of them.
	Keys keystore.Store
}

// WithDefaults returns c with zero values replaced by the defaults.
func (c Config) WithDefaults() Config {
	if c.MaxConnections == 0 {
		c.MaxConnections = defaultMaxConnections
	}
	if c.MaxSessionsPerConnection == 0 {
		c.MaxSessionsPerConnection = defaultMaxSessionsPerConnection
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = defaultMaxSessions
	}
	if c.ServWait == 0 {
		c.ServWait = defaultServWait
	}
	if c.RefWait == 0 {
		c.RefWait = defaultRefWait
	}
	if c.PartialMessageTimeout == 0 {
		c.PartialMessageTimeout = defaultPartialMessageTimeout
	}
	if c.TestPorts == (PortRange{}) {
		c.TestPorts = defaultTestPorts
	}
	if c.MaxPadding == 0 {
		c.MaxPadding = defaultMaxPadding
	}
	if c.Count == 0 {
		c.Count = wire.GreetingCountDefault
	}
	if c.Modes == 0 {
		c.Modes = wire.ModeUnauthenticated
	}
	if c.Rand == nil {
		c.Rand = rand.Reader
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Validate checks a configuration that already has its defaults.
func (c Config) Validate() error {
	var errs []error
	for _, p := range c.ACL {
		if !p.IsValid() {
			errs = append(errs, fmt.Errorf("acl entry %v is not a prefix", p))
		}
	}
	if c.MaxConnections < 0 || c.MaxSessionsPerConnection < 0 || c.MaxSessions < 0 {
		errs = append(errs, errors.New("connection and session limits must not be negative"))
	}
	if c.ServWait < 0 || c.RefWait < 0 || c.PartialMessageTimeout < 0 {
		errs = append(errs, errors.New("servwait, refwait and partial message timeout must not be negative"))
	}
	if c.TestPorts.Low == 0 || c.TestPorts.Low > c.TestPorts.High {
		errs = append(errs, fmt.Errorf("test port range %d-%d is empty or starts at 0", c.TestPorts.Low, c.TestPorts.High))
	}
	if c.Count < wire.GreetingCountMin || c.Count&(c.Count-1) != 0 {
		errs = append(errs, fmt.Errorf("greeting count %d is not a power of 2 of at least %d", c.Count, wire.GreetingCountMin))
	}
	errs = append(errs, c.validateModes())
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("control server config: %w", err)
	}
	return nil
}

// Status is a snapshot of the server.
type Status struct {
	// ConnectionsActive is the number of open control connections.
	ConnectionsActive int
	// ConnectionsAccepted counts connections that got a usable greeting.
	ConnectionsAccepted uint64
	// ConnectionsRejected counts connections refused by the ACL or the limit.
	ConnectionsRejected uint64
	// SessionsActive is the number of test sessions not yet released.
	SessionsActive int
}

// Server is a TWAMP-Control server.
type Server struct {
	cfg       Config
	handler   SessionHandler
	log       *slog.Logger
	acl       []netip.Prefix
	startTime ntptime.Timestamp

	mu       sync.Mutex
	conns    map[*conn]struct{}
	sessions map[wire.SID]*session
	accepted uint64
	rejected uint64
	serving  int // Serve calls running
	ivs      ivHistory
	// drainTimers joins detached session releases, including those already
	// removed from sessions when shutdown starts.
	drainTimers sync.WaitGroup
}

// New returns a server that opens test sessions through handler.
func New(cfg Config, handler SessionHandler, logger *slog.Logger) (*Server, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("control server: nil session handler")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	start := cfg.StartTime
	if start.IsZero() {
		start = cfg.Now()
	}
	s := &Server{
		cfg:       cfg,
		handler:   handler,
		log:       logger,
		startTime: ntptime.FromTime(start),
		conns:     make(map[*conn]struct{}),
		sessions:  make(map[wire.SID]*session),
	}
	for _, p := range cfg.ACL {
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		s.acl = append(s.acl, p.Masked())
	}
	return s, nil
}

// Accept retry backoff for temporary errors, as net/http.Server.Serve does.
const (
	minAcceptDelay = 5 * time.Millisecond
	maxAcceptDelay = time.Second
)

// Serve accepts control connections on every listener until ctx is done, then
// closes the listeners, the connections they accepted and every session, and
// returns. Pass all listeners (such as the IPv4 and IPv6 ones) to one call.
//
// A temporary Accept error, such as running out of file descriptors, is
// retried after a backoff. Any other error stops only that listener; it is
// returned when Serve ends, which is when ctx is done or every listener has
// stopped. Concurrent calls are safe: the last one to end releases the
// sessions left over.
func (s *Server) Serve(ctx context.Context, lns ...net.Listener) error {
	if len(lns) == 0 {
		return errors.New("control server: Serve needs a listener")
	}
	s.mu.Lock()
	s.serving++
	s.mu.Unlock()

	// connCtx ends the connections of this call when it stops, whether ctx is
	// done or every listener failed.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var conns, accepts sync.WaitGroup
	errs := make([]error, len(lns))
	for i, ln := range lns {
		stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
		defer stop()
		accepts.Go(func() { errs[i] = s.acceptLoop(ctx, connCtx, ln, &conns) })
	}
	accepts.Wait()
	for _, ln := range lns {
		_ = ln.Close()
	}
	cancel()
	conns.Wait()

	s.mu.Lock()
	s.serving--
	last := s.serving == 0
	s.mu.Unlock()
	if last {
		s.shutdown()
	}
	return errors.Join(errs...)
}

// acceptLoop accepts connections on ln until ctx is done or ln fails for
// good, serving each with connCtx. ctx decides whether an Accept error is the
// shutdown: canceling it sets its error before its AfterFunc closes ln, which
// is not so for connCtx, a child canceled in no particular order.
func (s *Server) acceptLoop(ctx, connCtx context.Context, ln net.Listener, conns *sync.WaitGroup) error {
	var delay time.Duration
	for {
		nc, err := ln.Accept()
		if err == nil {
			delay = 0
			conns.Go(func() { s.ServeConn(connCtx, nc) })
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if !temporary(err) {
			s.log.Error("control server: listener stopped", "listener", ln.Addr(), "err", err)
			return fmt.Errorf("control server: accept on %v: %w", ln.Addr(), err)
		}
		delay = min(max(2*delay, minAcceptDelay), maxAcceptDelay)
		s.log.Warn("control server: accept failed; retrying", "listener", ln.Addr(), "err", err, "delay", delay)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
	}
}

// temporary reports whether an Accept error may go away by itself: running
// out of file descriptors or memory, or a connection aborted before Accept.
func temporary(err error) bool {
	for _, e := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.ECONNABORTED, syscall.EAGAIN, syscall.EINTR} {
		if errors.Is(err, e) {
			return true
		}
	}
	var t interface{ Temporary() bool }
	return errors.As(err, &t) && t.Temporary()
}

// shutdown closes every connection and releases every session.
func (s *Server) shutdown() {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	sessions := make([]*session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.nc.Close()
	}
	for _, ss := range sessions {
		s.release(ss)
	}
	// All control workers have joined, so no new drain timer can be added.
	s.drainTimers.Wait()
}

// Status returns the connection and session counts.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		ConnectionsActive:   len(s.conns),
		ConnectionsAccepted: s.accepted,
		ConnectionsRejected: s.rejected,
		SessionsActive:      len(s.sessions),
	}
}

// Sessions returns a snapshot of the sessions not yet released, unordered.
func (s *Server) Sessions() []SessionInfo {
	s.mu.Lock()
	out := make([]SessionInfo, 0, len(s.sessions))
	handles := make([]Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		out = append(out, ss.info)
		handles = append(handles, ss.h)
	}
	s.mu.Unlock()
	for i, h := range handles {
		out[i].Stats = h.Stats()
	}
	return out
}

// session is the server's record of one test session.
type session struct {
	info       SessionInfo
	h          Session
	owner      *conn
	startedAt  time.Time
	drainUntil time.Time
	released   bool
	timer      *time.Timer // releases a session left by a closed connection
}

// sidAttempts bounds the retries when a new SID collides with a live one.
const sidAttempts = 8

// newSID returns a SID no live session uses.
func (s *Server) newSID(addr [4]byte, now time.Time) (wire.SID, error) {
	for range sidAttempts {
		sid, err := wire.NewSID(addr, ntptime.FromTime(now), s.cfg.Rand)
		if err != nil {
			return wire.SID{}, err
		}
		s.mu.Lock()
		_, taken := s.sessions[sid]
		s.mu.Unlock()
		if !taken {
			return sid, nil
		}
	}
	return wire.SID{}, errors.New("no unique SID")
}

// register adds a session unless the server-wide limit is reached or its SID
// was taken meanwhile.
func (s *Server) register(ss *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.sessions[ss.info.SID]; taken || len(s.sessions) >= s.cfg.MaxSessions {
		return false
	}
	s.sessions[ss.info.SID] = ss
	return true
}

// release closes a session once and forgets it.
func (s *Server) release(ss *session) {
	s.mu.Lock()
	if ss.released {
		s.mu.Unlock()
		return
	}
	ss.released = true
	delete(s.sessions, ss.info.SID)
	if ss.timer != nil && ss.timer.Stop() {
		s.drainTimers.Done()
	}
	s.mu.Unlock()
	st := ss.h.Stats()
	if err := ss.h.Close(); err != nil {
		s.log.Warn("control server: closing session", "sid", ss.info.SID, "err", err)
	}
	s.log.Info("control server: session released", "sid", ss.info.SID, "peer", ss.info.ControlPeer,
		"received", st.Received, "reflected", st.Reflected)
}

// setState records a session state change for Sessions.
func (s *Server) setState(ss *session, st SessionState) {
	s.mu.Lock()
	ss.info.State = st
	s.mu.Unlock()
}

// admit registers a new connection unless the ACL or the connection limit
// refuses it.
// The reasons admit refuses a connection, for the log (SPEC §9.8).
const (
	refusedACL   = "acl"
	refusedLimit = "max_connections"
)

func (s *Server) admit(c *conn) (refused string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.allowed(c.peer.Addr()):
		refused = refusedACL
	case len(s.conns) >= s.cfg.MaxConnections:
		refused = refusedLimit
	default:
		s.conns[c] = struct{}{}
		s.accepted++
		return ""
	}
	s.rejected++
	return refused
}

func (s *Server) allowed(a netip.Addr) bool {
	if len(s.acl) == 0 {
		return true
	}
	for _, p := range s.acl {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// leave forgets a connection.
func (s *Server) leave(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// addrPortOf converts a net.Addr to a netip.AddrPort with an unmapped
// address; it is invalid for addresses that are not IP endpoints.
func addrPortOf(a net.Addr) netip.AddrPort {
	var ap netip.AddrPort
	switch a := a.(type) {
	case *net.TCPAddr:
		ap = a.AddrPort()
	case nil:
		return ap
	default:
		ap, _ = netip.ParseAddrPort(a.String())
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
