//declscope:namespace server

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// connState is the server state of one control connection (SPEC §5.7).
type connState uint8

const (
	stateSetup       connState = iota // greeting sent, waiting for Set-Up-Response
	stateRequestWait                  // between sessions
	stateActive                       // sessions started
)

var (
	errServWait       = errors.New("servwait expired")
	errProtocol       = errors.New("protocol violation")
	errUnknownCommand = errors.New("unknown command")
	errSetupRefused   = errors.New("client chose mode 0")
)

// conn is one control connection.
type conn struct {
	s            *Server
	nc           net.Conn
	peer, local  netip.AddrPort
	state        connState
	lastActivity time.Time            // last control message, for SERVWAIT
	sessions     []*session           // not yet released
	sec          wire.ControlSecurity // the layer under every message after Set-Up-Response
	mode         wire.SecurityMode    // negotiated in Set-Up-Response
	features     wire.Modes           // the extension bits negotiated with it
	keys         wire.SessionKeys     // of the authenticated, encrypted and mixed modes
	// buf holds one message: the largest is a Set-Up-Response, or an RFC
	// 5938 message listing every session the connection may have.
	buf []byte
}

// ServeConn runs the server side of one control connection until it closes
// or ctx is done. Serve calls it for every accepted connection.
func (s *Server) ServeConn(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	c := &conn{
		s:            s,
		nc:           nc,
		peer:         addrPortOf(nc.RemoteAddr()),
		local:        addrPortOf(nc.LocalAddr()),
		lastActivity: s.cfg.Now(),
		buf:          make([]byte, max(wire.SetUpResponseSize, wire.SessionControlSize(s.cfg.MaxSessionsPerConnection))),
	}
	if reason := s.admit(c); reason != "" {
		c.refuse(reason)
		return
	}
	s.log.Info("control server: connection accepted", "peer", c.peer, "local", c.local)
	defer s.leave(c)
	defer c.abandon()
	switch err := c.run(); {
	case err == nil || ctx.Err() != nil:
	case errors.Is(err, wire.ErrIntegrity):
		// A failed HMAC drops the connection (RFC 4656 §6.10); it is either
		// corruption or tampering, which the operator should see.
		s.log.Warn("control server: message failed its integrity check", "peer", c.peer, "mode", c.mode, "err", err)
	default:
		// One line per connection, with why it ended: the client hanging up
		// (EOF), SERVWAIT, a partial message timeout or a protocol violation.
		s.log.Info("control server: connection closed", "peer", c.peer, "reason", err)
	}
}

// refuse sends a Server Greeting with Modes 0 (SPEC §5.7: ACL or limit).
func (c *conn) refuse(reason string) {
	g, err := wire.NewServerGreeting(0, c.s.cfg.Count, c.s.cfg.Rand)
	if err == nil {
		err = c.write(g.Append(c.buf[:0]))
	}
	c.s.log.Info("control server: connection refused", "peer", c.peer, "reason", reason, "err", err)
}

func (c *conn) run() error {
	g, err := wire.NewServerGreeting(c.s.cfg.Modes, c.s.cfg.Count, c.s.cfg.Rand)
	if err != nil {
		return err
	}
	if err := c.write(g.Append(c.buf[:0])); err != nil {
		return err
	}
	b, err := c.readMessage(func([]byte) int { return wire.SetUpResponseSize }, false)
	if err != nil {
		return err
	}
	var setup wire.SetUpResponse
	_ = setup.Decode(b) // cannot fail: b holds the whole message
	if setup.Mode == 0 {
		return errSetupRefused
	}
	mode, keys, refusal := c.negotiate(&g, &setup)
	accept := wire.AcceptOK
	if refusal != nil {
		accept = wire.AcceptFailure
	}
	ss, err := wire.NewServerStart(accept, c.s.startTime, c.s.cfg.Rand)
	if err != nil {
		return err
	}
	out := ss.Append(c.buf[:0])
	if accept == wire.AcceptOK {
		c.mode, c.keys, c.features = mode, keys, setup.Mode&FeatureModes
		c.sec = wire.Unauthenticated{}
		if mode != wire.SecurityUnauthenticated {
			c.sec = wire.NewControlCrypto(keys, ss.ServerIV, setup.ClientIV)
		}
		// The Start-Time block opens the server's protected stream
		// (RFC 4656 §3.1); it has no HMAC field of its own.
		if err := c.sec.Seal(out[32:48], false); err != nil {
			return err
		}
	}
	if err := c.write(out); err != nil {
		return err
	}
	if refusal != nil {
		c.s.log.Info("control server: set-up refused", "peer", c.peer, "mode", setup.Mode,
			"key_id", wire.KeyIDString(setup.KeyID), "reason", refusal)
		return refusal
	}
	if mode != wire.SecurityUnauthenticated {
		c.s.log.Info("control server: client authenticated", "peer", c.peer, "mode", mode, "key_id", wire.KeyIDString(setup.KeyID))
	}
	c.state = stateRequestWait
	for {
		b, err := c.readMessage(c.messageSize, true)
		if errors.Is(err, errUnknownCommand) {
			if c.state == stateRequestWait {
				// RFC 5357 §3.5; the length is unknown, so the stream is lost.
				reply := wire.AcceptSession{Accept: wire.AcceptNotSupported}
				_ = c.send(reply.Append(c.buf[:0]))
			}
			return fmt.Errorf("%w %v", err, wire.Command(c.buf[0]))
		}
		if err != nil {
			return err
		}
		c.lastActivity = c.s.cfg.Now()
		if err := c.dispatch(b); err != nil {
			return err
		}
	}
}

// messageSize returns the length of the client message whose header is h,
// or 0 for a command the connection does not take. Under Individual Session
// Control the length of Start-N-Sessions and Stop-N-Sessions follows from
// their Number of Sessions.
func (c *conn) messageSize(h []byte) int {
	cmd := wire.Command(h[0])
	if c.isc() && (cmd == wire.CommandStartNSessions || cmd == wire.CommandStopNSessions) {
		n := wire.SessionControlCount(h)
		if n > uint32(c.s.cfg.MaxSessionsPerConnection) { //nolint:gosec // G115: a positive configured limit
			return len(c.buf) + 1 // more sessions than the connection can have: refused by readMessage
		}
		return wire.SessionControlSize(int(n))
	}
	return cmd.MessageSize()
}

// isc reports whether Individual Session Control (RFC 5938) was negotiated.
func (c *conn) isc() bool { return c.features.Has(wire.ModeIndividualSessionControl) }

func (c *conn) dispatch(b []byte) error {
	cmd := wire.Command(b[0])
	switch {
	case c.isc() && (cmd == wire.CommandStartSessions || cmd == wire.CommandStopSessions):
		// RFC 5938 §3.1: the commands of RFC 5357 MUST NOT be used.
		return fmt.Errorf("%w: %v under individual session control", errProtocol, cmd)
	case cmd == wire.CommandStartNSessions:
		return c.handleStartN(b)
	case cmd == wire.CommandStopNSessions:
		return c.handleStopN(b)
	case cmd == wire.CommandStopSessions:
		return c.handleStop(b)
	case c.state == stateActive && !c.isc():
		return fmt.Errorf("%w: %v while sessions run", errProtocol, cmd)
	case cmd == wire.CommandRequestTWSession:
		return c.handleRequest(b)
	default: // CommandStartSessions, the only other command with a size
		return c.handleStart()
	}
}

// handleRequest answers Request-TW-Session (SPEC §5.7, §5.8). A rejected
// request keeps the connection open.
func (c *conn) handleRequest(b []byte) error {
	var req wire.RequestTWSession
	_ = req.Decode(b) // cannot fail: dispatched on command 5 with the full length
	reply := wire.AcceptSession{Accept: c.validate(&req)}
	if c.features.Has(wire.ModeReflectOctets) {
		// RFC 6038 §4.3: the octets come back whatever the outcome.
		reply.ReflectedOctets = req.OctetsToReflect
	}
	if reply.Accept != wire.AcceptOK {
		c.s.log.Info("control server: request refused", "peer", c.peer, "accept", reply.Accept,
			"reason", "unsupported request", "ipvn", req.IPVN, "padding", req.PaddingLength)
	} else {
		var err error
		reply.Accept, reply.Port, reply.SID, err = c.accept(&req)
		if err != nil {
			c.s.log.Info("control server: request refused", "peer", c.peer, "accept", reply.Accept, "err", err)
		}
	}
	return c.send(reply.Append(c.buf[:0]))
}

// validate returns AcceptNotSupported for requests TWAMP forbids or the
// configuration refuses (SPEC §5.5).
func (c *conn) validate(r *wire.RequestTWSession) wire.Accept {
	_, dscpOnly := r.TypeP.DSCP()
	// The session uses the address family of the control connection
	// (SPEC §5.8); an IPv4-mapped connection counts as IPv4.
	familyMismatch := c.local.IsValid() && (r.IPVN == wire.IPVN4) != c.local.Addr().Is4()
	switch {
	case r.ConfSender != 0, r.ConfReceiver != 0, r.ScheduleSlots != 0, r.Packets != 0,
		r.IPVN != wire.IPVN4 && r.IPVN != wire.IPVN6, familyMismatch, !dscpOnly, r.PaddingLength > c.s.cfg.MaxPadding,
		c.layout(r).Validate(c.mode.TestMode(), r.PaddingLength) != nil:
		return wire.AcceptNotSupported
	default:
		return wire.AcceptOK
	}
}

// accept opens and registers the session of a valid request.
func (c *conn) accept(r *wire.RequestTWSession) (wire.Accept, uint16, wire.SID, error) {
	if len(c.live()) >= c.s.cfg.MaxSessionsPerConnection {
		return wire.AcceptPermanentLimit, 0, wire.SID{}, errors.New("sessions per connection limit")
	}
	if c.s.Status().SessionsActive >= c.s.cfg.MaxSessions {
		return wire.AcceptTemporaryLimit, 0, wire.SID{}, errors.New("server session limit")
	}
	now := c.s.cfg.Now()
	local := requestAddr(r.IPVN, r.ReceiverAddress)
	if !local.IsValid() {
		local = c.local.Addr()
	}
	sender := requestAddr(r.IPVN, r.SenderAddress)
	if !sender.IsValid() {
		sender = c.peer.Addr()
	}
	sid, err := c.s.newSID(sidAddr(local), now)
	if err != nil {
		return wire.AcceptInternalError, 0, wire.SID{}, err
	}
	dscp, _ := r.TypeP.DSCP()
	spec := SessionSpec{
		SID:           sid,
		Local:         netip.AddrPortFrom(local, 0),
		Sender:        netip.AddrPortFrom(sender, r.SenderPort),
		ControlPeer:   c.peer,
		DSCP:          dscp,
		PaddingLength: r.PaddingLength,
		Timeout:       r.Timeout.Duration(),
		Security:      c.mode.TestMode(),
		Layout:        c.layout(r),
	}
	if spec.Security != wire.SecurityUnauthenticated {
		spec.TestKeys = wire.DeriveTestKeys(c.keys, sid)
	}
	if !r.StartTime.IsZero() {
		spec.StartTime = r.StartTime.Time(now)
	}
	h, bound, accept, err := c.s.openSession(spec, r.ReceiverPort)
	if err != nil {
		return accept, 0, wire.SID{}, err
	}
	ss := &session{
		h:     h,
		owner: c,
		info: SessionInfo{
			SID: sid, ControlPeer: c.peer, Sender: spec.Sender, Reflector: bound, State: SessionAccepted,
			Padding: r.PaddingLength, DSCP: dscp, Timeout: spec.Timeout, AcceptedAt: now, Security: c.mode,
		},
	}
	if !c.s.register(ss) {
		_ = h.Close()
		return wire.AcceptTemporaryLimit, 0, wire.SID{}, errors.New("server session limit")
	}
	c.sessions = append(c.sessions, ss)
	c.s.log.Info("control server: session accepted", "peer", c.peer, "sid", sid,
		"sender", spec.Sender, "reflector", bound, "padding", r.PaddingLength, "dscp", dscp, "timeout", spec.Timeout)
	return wire.AcceptOK, bound.Port(), sid, nil
}

// requestAddr reads an address field of Request-TW-Session; all zero is
// invalid, meaning "use the control connection's address".
func requestAddr(ipvn uint8, a [16]byte) netip.Addr {
	if a == [16]byte{} {
		return netip.Addr{}
	}
	if ipvn == wire.IPVN4 {
		return netip.AddrFrom4([4]byte(a[:4]))
	}
	return netip.AddrFrom16(a).Unmap()
}

// sidAddr returns the 4 octets of a that go in a SID (RFC 4656 §3.5): the
// IPv4 address, or the last 4 octets of an IPv6 one.
func sidAddr(a netip.Addr) [4]byte {
	b := a.As16()
	return [4]byte(b[12:])
}

// handleStart answers Start-Sessions: every accepted session starts.
func (c *conn) handleStart() error {
	ack := wire.StartAck{Accept: wire.AcceptFailure}
	now := c.s.cfg.Now()
	for _, ss := range c.live() {
		if ss.info.State == SessionAccepted {
			c.start(ss, now)
			ack.Accept = wire.AcceptOK
		}
	}
	if ack.Accept == wire.AcceptOK {
		c.state = stateActive
	}
	return c.send(ack.Append(c.buf[:0]))
}

// start begins reflecting for an accepted session.
func (c *conn) start(ss *session, now time.Time) {
	ss.h.Start(now)
	ss.startedAt = now
	c.s.setState(ss, SessionActive)
}

// handleStop applies Stop-Sessions: the started sessions drain for their
// Timeout. A count that does not match closes the connection.
func (c *conn) handleStop(b []byte) error {
	var stop wire.StopSessions
	_ = stop.Decode(b) // cannot fail: dispatched on command 3 with the full length
	var active []*session
	for _, ss := range c.live() {
		if ss.info.State == SessionActive {
			active = append(active, ss)
		}
	}
	if int(stop.Sessions) != len(active) {
		return fmt.Errorf("%w: stop-sessions for %d sessions, %d started", errProtocol, stop.Sessions, len(active))
	}
	if stop.Accept != wire.AcceptOK {
		c.s.log.Info("control server: client stopped with failure", "peer", c.peer, "accept", stop.Accept)
	}
	now := c.s.cfg.Now()
	for _, ss := range active {
		c.drain(ss, now)
	}
	c.state = stateRequestWait
	return nil
}

// drain stops a started session: it reflects for its Timeout, then the
// server releases it. The wait is capped at REFWAIT: the Timeout comes from
// an unauthenticated client and may be up to 2^32 s, which would hold the
// session and its socket for good (RFC 4656 §6 resource exhaustion).
func (c *conn) drain(ss *session, now time.Time) {
	ss.drainUntil = now.Add(min(ss.info.Timeout, c.s.cfg.RefWait))
	ss.h.Stop(ss.drainUntil)
	c.s.setState(ss, SessionDraining)
}

// live returns the sessions of the connection not yet released.
func (c *conn) live() []*session {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	kept := c.sessions[:0]
	for _, ss := range c.sessions {
		if !ss.released {
			kept = append(kept, ss)
		}
	}
	c.sessions = kept
	return kept
}

// lastActive returns when a started session last saw a packet, for REFWAIT.
func (ss *session) lastActive() time.Time {
	if t := ss.h.Stats().LastReceived; t.After(ss.startedAt) {
		return t
	}
	return ss.startedAt
}

// nextTimer returns when the connection next has something to do while idle:
// SERVWAIT outside sessions, REFWAIT of started sessions, and the release of
// draining ones.
func (c *conn) nextTimer() time.Time {
	var next time.Time
	earliest := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	if c.state != stateActive {
		earliest(c.lastActivity.Add(c.s.cfg.ServWait))
	}
	for _, ss := range c.live() {
		switch ss.info.State {
		case SessionActive:
			earliest(ss.lastActive().Add(c.s.cfg.RefWait))
		case SessionDraining:
			earliest(ss.drainUntil)
		}
	}
	return next
}

// onTimer releases drained sessions and those past REFWAIT, and reports
// SERVWAIT expiry.
func (c *conn) onTimer(now time.Time) error {
	active := 0
	for _, ss := range c.live() {
		switch ss.info.State {
		case SessionDraining:
			if !now.Before(ss.drainUntil) {
				c.s.release(ss)
			}
		case SessionActive:
			if now.Sub(ss.lastActive()) >= c.s.cfg.RefWait {
				c.s.log.Info("control server: refwait expired", "sid", ss.info.SID)
				c.s.release(ss)
			} else {
				active++
			}
		}
	}
	if c.state == stateActive && active == 0 {
		// Every session timed out: resume as if Stop-Sessions had come.
		c.state = stateRequestWait
		c.lastActivity = now
	}
	if c.state != stateActive && now.Sub(c.lastActivity) >= c.s.cfg.ServWait {
		return errServWait
	}
	return nil
}

// abandon handles the sessions of a closed connection: accepted ones are
// released at once, started ones drain for their Timeout first (SPEC §5.7).
func (c *conn) abandon() {
	now := c.s.cfg.Now()
	for _, ss := range c.live() {
		switch ss.info.State {
		case SessionAccepted:
			c.s.release(ss)
			continue
		case SessionActive:
			c.drain(ss, now)
		}
		c.s.mu.Lock()
		if !ss.released {
			ss.timer = time.AfterFunc(ss.drainUntil.Sub(now), func() { c.s.release(ss) })
		}
		c.s.mu.Unlock()
	}
}

// readMessage reads one message into c.buf. sizeOf returns the length from
// the first ControlHeaderSize octets, or 0 for an unknown command. Waiting
// for the first octet runs the idle timers; once a message has begun, the
// rest must arrive within the partial message timeout. With open, the
// message goes through the ControlSecurity: the header is opened before its
// length is read from it, and the HMAC is checked at the end.
func (c *conn) readMessage(sizeOf func(header []byte) int, open bool) ([]byte, error) {
	for {
		if err := c.nc.SetReadDeadline(c.wallClock(c.nextTimer())); err != nil {
			return nil, err
		}
		n, err := c.nc.Read(c.buf[:1])
		if n == 1 {
			break
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, err
		}
		if err := c.onTimer(c.s.cfg.Now()); err != nil {
			return nil, err
		}
	}
	if err := c.nc.SetReadDeadline(time.Now().Add(c.s.cfg.PartialMessageTimeout)); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(c.nc, c.buf[1:wire.ControlHeaderSize]); err != nil {
		return nil, err
	}
	if open {
		if err := c.sec.Open(c.buf[:wire.ControlHeaderSize]); err != nil {
			return nil, err
		}
	}
	size := sizeOf(c.buf[:wire.ControlHeaderSize])
	switch {
	case size == 0:
		return nil, errUnknownCommand
	case size > len(c.buf):
		return nil, fmt.Errorf("%w: a %d-octet message", errProtocol, size)
	}
	if _, err := io.ReadFull(c.nc, c.buf[wire.ControlHeaderSize:size]); err != nil {
		return nil, err
	}
	if open {
		if err := c.sec.Open(c.buf[wire.ControlHeaderSize:size]); err != nil {
			return nil, err
		}
		if err := c.sec.Verify(c.buf[:size]); err != nil {
			return nil, err
		}
	}
	return c.buf[:size], nil
}

// negotiate checks the mode the client chose in setup against the greeting
// g: exactly one security mode, nothing that was not offered, and for the
// authenticated, encrypted and mixed modes a valid KeyID and Token.
func (c *conn) negotiate(g *wire.ServerGreeting, setup *wire.SetUpResponse) (wire.SecurityMode, wire.SessionKeys, error) {
	mode, ok := setup.Mode.Security()
	if !ok || setup.Mode&^g.Modes != 0 {
		return 0, wire.SessionKeys{}, fmt.Errorf("%w: %v", errModeNotOffered, setup.Mode)
	}
	if mode == wire.SecurityUnauthenticated {
		return mode, wire.SessionKeys{}, nil
	}
	keys, err := c.s.authenticate(g, setup)
	return mode, keys, err
}

// send seals a reply, all of which end with an HMAC field, and writes it.
func (c *conn) send(msg []byte) error {
	if err := c.sec.Seal(msg, true); err != nil {
		return err
	}
	return c.write(msg)
}

// wallClock converts t, a time of Config.Now, to the real clock that
// net.Conn deadlines follow, so that an injected clock does not shift them.
func (c *conn) wallClock(t time.Time) time.Time {
	return time.Now().Add(t.Sub(c.s.cfg.Now()))
}

// write sends b within the partial message timeout.
func (c *conn) write(b []byte) error {
	if err := c.nc.SetWriteDeadline(time.Now().Add(c.s.cfg.PartialMessageTimeout)); err != nil {
		return err
	}
	_, err := c.nc.Write(b)
	return err
}
