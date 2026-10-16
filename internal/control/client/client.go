// Package client implements the TWAMP-Control Control-Client (RFC 5357 §3,
// SPEC §5.6) in the unauthenticated, authenticated, encrypted and mixed
// modes (RFC 4656 §3.1, RFC 5618).
//
// A Client runs over a connected net.Conn. Handshake reads the Server
// Greeting and completes the mode negotiation; the client is then IDLE and
// may request any number of sessions before Start. After Stop it is IDLE
// again, so one connection can carry several runs (SPEC §13).
package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Defaults of Config and Request (SPEC §5.6, §5.9, appendix A).
const (
	DefaultTimeout        = 15 * time.Second // control_timeout
	defaultMaxCount       = wire.GreetingCountMax
	defaultSessionTimeout = 2 * time.Second
)

// Config configures a Client. Zero values take the defaults.
type Config struct {
	// Timeout bounds every wait for a server message (control_timeout).
	Timeout time.Duration
	// MaxCount is the largest Server Greeting Count accepted (RFC 5357 §6).
	//
	//declscope:ignore overexported // RFC 5357 §6 asks for the limit to be configurable; no configuration key sets it yet
	MaxCount uint32
	// RejectAlternatePort fails a request when the server offers a Receiver
	// Port other than the one requested (accept_alternate_port: false).
	RejectAlternatePort bool
	// Modes are the security modes the client accepts (Modes bits 0–3); it
	// chooses the strongest the server offers, in the order encrypted,
	// authenticated, mixed, unauthenticated, as twping does. Zero accepts
	// unauthenticated mode only.
	Modes wire.Modes
	// KeyID names the shared secret for the authenticated, encrypted and
	// mixed modes, as the identity of a pfs file.
	KeyID string
	// Passphrase is the shared secret of KeyID.
	Passphrase []byte
	// Rand supplies the session keys and the Client-IV; nil means
	// crypto/rand.
	Rand io.Reader
	// Features are the extensions the client asks for, of FeatureModes:
	// those the server offers are used, the others not (see
	// Client.Features).
	Features wire.Modes
}

// FeatureModes are the Modes bits beyond the security modes that the client
// implements: Individual Session Control (RFC 5938), Reflect Octets and
// Symmetrical Size (RFC 6038).
const FeatureModes = wire.ModeIndividualSessionControl | wire.ModeReflectOctets | wire.ModeSymmetricalSize

// preference is the order in which the client chooses a security mode.
var preference = []wire.SecurityMode{wire.SecurityEncrypted, wire.SecurityAuthenticated, wire.SecurityMixed, wire.SecurityUnauthenticated}

// choose returns the strongest security mode both sides accept.
func (cfg *Config) choose(offered wire.Modes) (wire.SecurityMode, bool) {
	for _, m := range preference {
		if cfg.Modes.Has(m.Mode()) && offered.Has(m.Mode()) {
			return m, true
		}
	}
	return 0, false
}

// Validate checks security settings without I/O. Zero values use the same defaults
// as Handshake; the configuration is not modified.
func (cfg Config) Validate() error {
	if cfg.Modes == 0 {
		cfg.Modes = wire.ModeUnauthenticated
	}
	if cfg.Modes&^wire.SecurityModes != 0 {
		return fmt.Errorf("modes %v: only the security modes can be chosen", cfg.Modes)
	}
	if cfg.Features&^FeatureModes != 0 {
		return fmt.Errorf("features %v: only individual session control, reflect octets and symmetrical size are implemented", cfg.Features)
	}
	if cfg.Modes&^wire.ModeUnauthenticated == 0 {
		return nil
	}
	if _, err := wire.KeyID(cfg.KeyID); err != nil {
		return fmt.Errorf("modes %v need a key id: %w", cfg.Modes, err)
	}
	if len(cfg.Passphrase) == 0 {
		return fmt.Errorf("modes %v need a passphrase", cfg.Modes)
	}
	return nil
}

// State is the Control-Client state (SPEC §5.6).
type State uint8

// Control-Client states.
//
//declscope:ignore overexported // the values State reports; the tests check the state machine with them
const (
	StateClosed State = iota
	StateIdle
	StateActive
)

// Request is a session request. Unset fields follow SPEC §5.9.
type Request struct {
	// SenderPort is the UDP port the caller has bound for the Session-Sender.
	SenderPort uint16
	// ReceiverPort is the port asked of the reflector; 0 asks for SenderPort.
	ReceiverPort uint16
	// SenderAddress defaults to the local address of the control connection.
	//
	//declscope:ignore overexported // explicit test addresses (SPEC §5.9), which RunFull leaves to the defaults
	SenderAddress netip.Addr
	// ReceiverAddress defaults to the server address of the control connection.
	//
	//declscope:ignore overexported // explicit test addresses (SPEC §5.9), which RunFull leaves to the defaults
	ReceiverAddress netip.Addr
	// ZeroAddresses sends both addresses as zero, for NAT traversal (CMP-01).
	ZeroAddresses bool
	// PaddingLength is the padding of every Session-Sender packet.
	PaddingLength uint32
	// StartTime is sent as the Start Time; zero sends 0, start immediately.
	StartTime time.Time
	// Timeout is how long the reflector keeps reflecting after Stop-Sessions;
	// 0 means 2 s (SPEC §5.9).
	Timeout time.Duration
	// OctetsToReflect are two octets the server must return in Accept-Session
	// (RFC 6038 §4.2); they need Reflect Octets.
	OctetsToReflect uint16
	// PaddingToReflect is the length of padding the reflector must return
	// verbatim (Reflect Octets); it must be below PaddingLength.
	PaddingToReflect uint16
	// DSCP is the Type-P DSCP (0–63) the reflector must use.
	DSCP uint8
}

// ValidateFeatures checks reflection fields against requested or negotiated features.
func (r Request) ValidateFeatures(features wire.Modes) error {
	if (r.OctetsToReflect != 0 || r.PaddingToReflect != 0) && !features.Has(wire.ModeReflectOctets) {
		return errors.New("octets to reflect need reflect octets mode, which was not requested or negotiated")
	}
	return nil
}

// ValidateLayout checks the request against one possible or negotiated test mode.
func (r Request) ValidateLayout(mode wire.SecurityMode, features wire.Modes) error {
	if err := r.ValidateFeatures(features); err != nil {
		return err
	}
	layout := wire.PaddingLayout{SymmetricalSize: features.Has(wire.ModeSymmetricalSize), ReflectLength: r.PaddingToReflect}
	return layout.Validate(mode.TestMode(), r.PaddingLength)
}

// Session is a session the server accepted.
type Session struct {
	// SID identifies the session.
	SID wire.SID
	// Reflector is where test packets go: the receiver address and the
	// port from Accept-Session.
	Reflector netip.AddrPort
	// AlternatePort reports that the server chose another Receiver Port.
	AlternatePort bool
	// Request is the Request-TW-Session as sent.
	Request wire.RequestTWSession
	// Security is the mode of the test packets: unauthenticated (also for a
	// connection in mixed mode), authenticated or encrypted.
	Security wire.SecurityMode
	// TestKeys are the keys of the test packets in the authenticated and
	// encrypted modes (RFC 5357 §4.2.1).
	TestKeys wire.TestKeys
	// Layout is the RFC 6038 layout of the test packets.
	Layout wire.PaddingLayout
	// ServerOctets are the octets the server asked for at the start of the
	// padding to be reflected (RFC 6038 §4.3); 0 for none.
	ServerOctets uint16
}

// Client is a Control-Client on one control connection. Its methods are not
// safe for concurrent use, except Lost and Err.
type Client struct {
	conn          net.Conn
	cfg           Config
	local, remote netip.AddrPort
	state         State
	greeting      wire.ServerGreeting
	serverStart   wire.ServerStart
	sec           wire.ControlSecurity // the layer under every message after Set-Up-Response
	mode          wire.SecurityMode    // negotiated in Set-Up-Response
	features      wire.Modes           // the extensions negotiated with it
	keys          wire.SessionKeys     // of the authenticated, encrypted and mixed modes
	pending       []*Session           // accepted, not yet started
	active        []*Session           // started, not yet stopped

	monitorDone chan struct{} // closed when the ACTIVE monitor returns
	monitoring  bool          // a monitor runs
	lost        chan struct{} // closed when the connection fails while ACTIVE
	lostOnce    sync.Once
	lostErr     error
}

// Handshake reads the Server Greeting from conn, chooses the security mode,
// proves the shared secret in the authenticated ones, and reads Server-Start
// (SPEC §5.6). On failure it closes conn.
func Handshake(ctx context.Context, conn net.Conn, cfg Config) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxCount == 0 {
		cfg.MaxCount = defaultMaxCount
	}
	if cfg.Modes == 0 {
		cfg.Modes = wire.ModeUnauthenticated
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	if err := cfg.Validate(); err != nil {
		_ = conn.Close()
		return nil, &Error{Stage: StageSetup, Err: err}
	}
	c := &Client{
		conn:   conn,
		cfg:    cfg,
		local:  addrPortOf(conn.LocalAddr()),
		remote: addrPortOf(conn.RemoteAddr()),
	}
	if err := c.handshake(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.state = StateIdle
	return c, nil
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

func (c *Client) handshake(ctx context.Context) error {
	var buf [wire.SetUpResponseSize]byte
	g := buf[:wire.ServerGreetingSize]
	if err := c.read(ctx, StageGreeting, g); err != nil {
		return err
	}
	if err := c.greeting.Decode(g); err != nil {
		return &Error{Stage: StageGreeting, Err: err}
	}
	if c.greeting.Modes == 0 {
		return &Error{Stage: StageGreeting, Err: ErrServerRefused}
	}
	if c.greeting.Count > c.cfg.MaxCount {
		return &Error{Stage: StageGreeting, Err: fmt.Errorf("%w: %d > %d", ErrCountTooLarge, c.greeting.Count, c.cfg.MaxCount)}
	}
	mode, ok := c.cfg.choose(c.greeting.Modes)
	var fail error
	switch {
	case !ok:
		fail = fmt.Errorf("%w: server offers %v, client accepts %v", ErrNoCommonMode, c.greeting.Modes, c.cfg.Modes)
	case mode != wire.SecurityUnauthenticated && c.greeting.Count < wire.GreetingCountMin:
		fail = fmt.Errorf("%w: %d < %d", ErrCountTooSmall, c.greeting.Count, wire.GreetingCountMin)
	}
	// Mode 0 tells the server the client will not continue (RFC 4656 §3.1).
	setup := wire.SetUpResponse{}
	if fail == nil {
		var err error
		if setup, err = c.setUp(mode); err != nil {
			return &Error{Stage: StageSetup, Err: err}
		}
	}
	if err := c.write(ctx, StageSetup, setup.Append(buf[:0])); err != nil {
		return err
	}
	if fail != nil {
		return &Error{Stage: StageGreeting, Err: fail}
	}
	s := buf[:wire.ServerStartSize]
	if err := c.read(ctx, StageSetup, s); err != nil {
		return err
	}
	if err := c.serverStart.Decode(s); err != nil {
		return &Error{Stage: StageSetup, Err: err}
	}
	if a := c.serverStart.Accept; a != wire.AcceptOK {
		return rejected(StageSetup, "server-start", a)
	}
	c.mode, c.features = mode, setup.Mode&FeatureModes
	c.sec = wire.Unauthenticated{}
	if mode != wire.SecurityUnauthenticated {
		c.sec = wire.NewControlCrypto(c.keys, setup.ClientIV, c.serverStart.ServerIV)
	}
	// The Start-Time block is the first of the server's protected stream
	// (RFC 4656 §3.1); the server leaves it clear when Accept is non-zero.
	if err := c.sec.Open(s[32:48]); err != nil {
		return &Error{Stage: StageSetup, Err: err}
	}
	if err := c.serverStart.Decode(s); err != nil {
		return &Error{Stage: StageSetup, Err: err}
	}
	return nil
}

// setUp returns the Set-Up-Response choosing mode. In the authenticated,
// encrypted and mixed modes it draws the session keys and the Client-IV and
// encrypts them with the Challenge into the Token (RFC 4656 §3.1).
func (c *Client) setUp(mode wire.SecurityMode) (wire.SetUpResponse, error) {
	setup := wire.SetUpResponse{Mode: mode.Mode() | c.cfg.Features&c.greeting.Modes}
	if mode == wire.SecurityUnauthenticated {
		return setup, nil
	}
	var err error
	if setup.KeyID, err = wire.KeyID(c.cfg.KeyID); err != nil {
		return setup, err
	}
	key, err := wire.DeriveKey(c.cfg.Passphrase, c.greeting.Salt, c.greeting.Count)
	if err != nil {
		return setup, err
	}
	if c.keys, err = wire.NewSessionKeys(c.cfg.Rand); err != nil {
		return setup, err
	}
	if _, err := io.ReadFull(c.cfg.Rand, setup.ClientIV[:]); err != nil {
		return setup, fmt.Errorf("client-iv: %w", err)
	}
	setup.Token = wire.EncryptToken(key, c.greeting.Challenge, c.keys)
	return setup, nil
}

func rejected(stage Stage, msg string, a wire.Accept) *Error {
	return &Error{Stage: stage, Accept: a, Err: fmt.Errorf("%w: %s accept=%d (%v)", ErrRejected, msg, uint8(a), a.Normalize())}
}

// Greeting returns the Server Greeting.
func (c *Client) Greeting() wire.ServerGreeting { return c.greeting }

// ServerStartTime returns the Start-Time of Server-Start: when the server
// instance started.
func (c *Client) ServerStartTime() ntptime.Timestamp { return c.serverStart.StartTime }

// Features returns the extensions negotiated: those of Config.Features the
// server offered.
func (c *Client) Features() wire.Modes { return c.features }

// Security returns the security mode of the control connection.
func (c *Client) Security() wire.SecurityMode { return c.mode }

// State returns the current state.
func (c *Client) State() State { return c.state }

// Request sends Request-TW-Session and waits for Accept-Session. A rejection
// closes the connection (SPEC §5.6). The session starts with the next Start.
func (c *Client) Request(ctx context.Context, r Request) (*Session, error) {
	// Under Individual Session Control a session may be requested while
	// others run (RFC 5938).
	if c.state != StateIdle && (c.state != StateActive || !c.isc()) {
		return nil, &Error{Stage: StageRequest, Err: ErrState}
	}
	req, reflectorAddr, err := c.buildRequest(r)
	if err != nil {
		return nil, &Error{Stage: StageRequest, Err: err}
	}
	if err := c.pause(); err != nil {
		return nil, err
	}
	defer c.resume()
	var buf [wire.RequestTWSessionSize]byte
	if err := c.send(ctx, StageRequest, req.Append(buf[:0])); err != nil {
		return nil, c.fail(err)
	}
	b := buf[:wire.AcceptSessionSize]
	if err := c.receive(ctx, StageRequest, b); err != nil {
		return nil, c.fail(err)
	}
	var as wire.AcceptSession
	if err := as.Decode(b); err != nil {
		return nil, c.fail(&Error{Stage: StageRequest, Err: err})
	}
	if as.Accept != wire.AcceptOK {
		err := rejected(StageRequest, "accept-session", as.Accept)
		if c.isc() {
			// The other sessions of the connection go on.
			return nil, err
		}
		return nil, c.fail(err)
	}
	if c.features.Has(wire.ModeReflectOctets) && as.ReflectedOctets != req.OctetsToReflect {
		return nil, c.fail(&Error{Stage: StageRequest, Err: fmt.Errorf("%w: %#04x for %#04x", ErrReflectedOctets, as.ReflectedOctets, req.OctetsToReflect)})
	}
	if as.Port == 0 {
		return nil, c.fail(&Error{Stage: StageRequest, Err: ErrInvalidPort})
	}
	s := &Session{
		SID: as.SID, Reflector: netip.AddrPortFrom(reflectorAddr, as.Port), Request: req, Security: c.mode.TestMode(),
		Layout: c.layout(&req), ServerOctets: as.ServerOctets,
	}
	if s.Security != wire.SecurityUnauthenticated {
		s.TestKeys = wire.DeriveTestKeys(c.keys, as.SID)
	}
	if as.Port != req.ReceiverPort {
		if c.cfg.RejectAlternatePort {
			return nil, c.fail(&Error{Stage: StageRequest, Err: fmt.Errorf("%w %d for %d", ErrAlternatePort, as.Port, req.ReceiverPort)})
		}
		s.AlternatePort = true
	}
	c.pending = append(c.pending, s)
	return s, nil
}

// buildRequest fills a Request-TW-Session by SPEC §5.9 and returns it with
// the address test packets go to.
func (c *Client) buildRequest(r Request) (wire.RequestTWSession, netip.Addr, error) {
	sender, receiver := r.SenderAddress.Unmap(), r.ReceiverAddress.Unmap()
	if !sender.IsValid() {
		sender = c.local.Addr()
	}
	if !receiver.IsValid() {
		receiver = c.remote.Addr()
	}
	if !receiver.IsValid() {
		return wire.RequestTWSession{}, netip.Addr{}, errors.New("no receiver address: the control connection is not over IP")
	}
	if sender.IsValid() && sender.Is4() != receiver.Is4() {
		return wire.RequestTWSession{}, netip.Addr{}, fmt.Errorf("sender address %v and receiver address %v differ in family", sender, receiver)
	}
	if r.DSCP > 63 {
		return wire.RequestTWSession{}, netip.Addr{}, fmt.Errorf("dscp %d is out of range 0-63", r.DSCP)
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultSessionTimeout
	}
	req := wire.RequestTWSession{
		IPVN:          wire.IPVN6,
		SenderPort:    r.SenderPort,
		ReceiverPort:  r.ReceiverPort,
		PaddingLength: r.PaddingLength,
		Timeout:       ntptime.FromDuration(timeout),
		TypeP:         wire.TypePFromDSCP(r.DSCP),
	}
	if receiver.Is4() {
		req.IPVN = wire.IPVN4
	}
	if req.ReceiverPort == 0 {
		req.ReceiverPort = r.SenderPort
	}
	if !r.StartTime.IsZero() {
		req.StartTime = ntptime.FromTime(r.StartTime)
	}
	if r.OctetsToReflect != 0 || r.PaddingToReflect != 0 {
		req.OctetsToReflect, req.PaddingToReflect = r.OctetsToReflect, r.PaddingToReflect
	}
	if err := r.ValidateLayout(c.mode.TestMode(), c.features); err != nil {
		return wire.RequestTWSession{}, netip.Addr{}, err
	}
	if !r.ZeroAddresses {
		// IPv4 goes in the first 4 octets; the rest stays zero (CMP-19).
		copy(req.SenderAddress[:], sender.AsSlice())
		copy(req.ReceiverAddress[:], receiver.AsSlice())
	}
	return req, receiver, nil
}

// Start sends Start-Sessions for every accepted session and waits for
// Start-Ack. The client is then ACTIVE: it sends nothing until Stop, but
// watches the connection, closing Lost if the server goes away. The sessions
// begin at max(now, their Start Time).
func (c *Client) Start(ctx context.Context) error {
	if c.state != StateIdle || len(c.pending) == 0 || c.isc() {
		return &Error{Stage: StageStart, Err: ErrState}
	}
	var buf [wire.StartSessionsSize]byte
	if err := c.send(ctx, StageStart, (&wire.StartSessions{}).Append(buf[:0])); err != nil {
		return c.fail(err)
	}
	if err := c.receive(ctx, StageStart, buf[:wire.StartAckSize]); err != nil {
		return c.fail(err)
	}
	var ack wire.StartAck
	_ = ack.Decode(buf[:]) // cannot fail: the buffer holds a full message
	if ack.Accept != wire.AcceptOK {
		return c.fail(rejected(StageStart, "start-ack", ack.Accept))
	}
	c.active = append(c.active, c.pending...)
	c.pending = nil
	c.activate()
	return nil
}

// activate enters ACTIVE: a new watch period whose loss Lost reports, and a
// monitor on the connection.
func (c *Client) activate() {
	c.state = StateActive
	c.lost = make(chan struct{})
	c.lostOnce = sync.Once{}
	c.lostErr = nil
	c.resume()
}

// resume starts the monitor while ACTIVE, after an exchange paused it.
func (c *Client) resume() {
	if c.state != StateActive || c.monitoring {
		return
	}
	c.monitoring = true
	c.monitorDone = make(chan struct{})
	_ = c.conn.SetReadDeadline(time.Time{}) // before the monitor runs, so pause's deadline wins
	go c.monitor()
}

// pause stops the monitor before an exchange on the connection. It returns
// the error, and closes the connection, if the monitor saw it fail.
func (c *Client) pause() error {
	if !c.monitoring {
		return nil
	}
	_ = c.conn.SetReadDeadline(time.Unix(1, 0)) // wake the monitor
	<-c.monitorDone
	c.monitoring = false
	if err := c.Err(); err != nil {
		c.state = StateClosed
		_ = c.conn.Close()
		return err
	}
	return nil
}

// monitor reads the connection while ACTIVE. The server sends nothing in
// this state, so any outcome other than the deadline set by Stop means the
// control connection is lost.
func (c *Client) monitor() {
	defer close(c.monitorDone)
	var b [1]byte
	n, err := c.conn.Read(b[:])
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	if n > 0 {
		err = errors.New("unexpected message from the server while sessions run")
	}
	c.lostOnce.Do(func() {
		c.lostErr = &Error{Stage: StageTest, Err: fmt.Errorf("%w: %w", ErrControlLost, err)}
		close(c.lost)
	})
}

// Lost returns a channel closed when the control connection fails while
// ACTIVE; Err then tells why. It is nil outside ACTIVE.
func (c *Client) Lost() <-chan struct{} { return c.lost }

// Err returns why Lost was closed, or nil.
func (c *Client) Err() error {
	select {
	case <-c.lost:
		return c.lostErr
	default:
		return nil
	}
}

// Started returns the sessions started and not yet stopped.
//
//declscope:ignore overexported // for callers running several sessions on one connection (SPEC §13); RunFull runs one
func (c *Client) Started() []*Session { return c.active }

// Stop sends Stop-Sessions for every started session, with accept AcceptOK
// for a normal end and anything else to mark the results invalid. The client
// is IDLE again and may request new sessions.
func (c *Client) Stop(ctx context.Context, accept wire.Accept) error {
	if c.state != StateActive || c.isc() {
		return &Error{Stage: StageStop, Err: ErrState}
	}
	if err := c.pause(); err != nil {
		return err
	}
	var buf [wire.StopSessionsSize]byte
	stop := wire.StopSessions{Accept: accept, Sessions: uint32(len(c.active))} //nolint:gosec // bounded by the sessions accepted on one connection
	if err := c.send(ctx, StageStop, stop.Append(buf[:0])); err != nil {
		return c.fail(err)
	}
	c.active = nil
	c.state = StateIdle
	return nil
}

// Close closes the control connection. Close it after Stop; closing while
// ACTIVE ends the sessions only when their REFWAIT expires on the server.
func (c *Client) Close() error {
	c.state = StateClosed
	err := c.conn.Close()
	if c.monitorDone != nil {
		<-c.monitorDone
	}
	return err
}

// fail closes the connection after a protocol failure and returns err.
func (c *Client) fail(err error) error {
	c.state = StateClosed
	_ = c.conn.Close()
	return err
}

// send seals a control message, all of which end with an HMAC field, and
// writes it.
func (c *Client) send(ctx context.Context, stage Stage, msg []byte) error {
	if err := c.sec.Seal(msg, true); err != nil {
		return &Error{Stage: stage, Err: err}
	}
	return c.write(ctx, stage, msg)
}

// receive reads a server message of len(msg) octets, opens it and checks
// its HMAC.
func (c *Client) receive(ctx context.Context, stage Stage, msg []byte) error {
	if err := c.read(ctx, stage, msg); err != nil {
		return err
	}
	if err := c.sec.Open(msg[:wire.ControlHeaderSize]); err != nil {
		return &Error{Stage: stage, Err: err}
	}
	if err := c.sec.Open(msg[wire.ControlHeaderSize:]); err != nil {
		return &Error{Stage: stage, Err: err}
	}
	if err := c.sec.Verify(msg); err != nil {
		return &Error{Stage: stage, Err: err}
	}
	return nil
}

// read fills b from the connection within the control timeout.
func (c *Client) read(ctx context.Context, stage Stage, b []byte) error {
	defer context.AfterFunc(ctx, func() { _ = c.conn.SetReadDeadline(time.Unix(1, 0)) })()
	// A failure to set the deadline (a closed connection) shows in the read.
	_ = c.conn.SetReadDeadline(time.Now().Add(c.cfg.Timeout)) // deadlines follow the real clock, not Config.Now
	if _, err := io.ReadFull(c.conn, b); err != nil {
		return c.ioError(ctx, stage, err)
	}
	return nil
}

// write sends b within the control timeout.
func (c *Client) write(ctx context.Context, stage Stage, b []byte) error {
	defer context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Unix(1, 0)) })()
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.cfg.Timeout))
	if _, err := c.conn.Write(b); err != nil {
		return c.ioError(ctx, stage, err)
	}
	return nil
}

func (c *Client) ioError(ctx context.Context, stage Stage, err error) error {
	switch {
	case ctx.Err() != nil:
		return &Error{Stage: stage, Err: ctx.Err()}
	case errors.Is(err, os.ErrDeadlineExceeded):
		return &Error{Stage: stage, Err: fmt.Errorf("%w in %v (after %v)", ErrTimeout, stage, c.cfg.Timeout)}
	default:
		return &Error{Stage: stage, Err: err}
	}
}
