//declscope:namespace sender

package sender

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Defaults of a Full run (SPEC §5.1, §5.6, §5.9, appendix A).
const (
	defaultControlPort    = 862
	defaultControlTimeout = client.DefaultTimeout
)

// TCP keepalive of the control connection (SPEC §5.1, the Junos defaults).
const (
	keepAliveIdle     = 120 * time.Second
	keepAliveInterval = 5 * time.Second
	keepAliveCount    = 6
)

// stopTimeout bounds sending Stop-Sessions after the run's context ended. It
// leaves room in the 5 s that gotwampd has to shut down (SPEC §9.1).
const stopTimeout = 2 * time.Second

// PortRange is an inclusive range of UDP ports.
type PortRange struct {
	Low, High uint16
}

// defaultSenderPorts is where the Sender Port of a Full run comes from: the
// ports both SR Linux / Junos Evolved (49152–65535) and IOS-XR (≤ 57343)
// accept (CMP-21).
var defaultSenderPorts = PortRange{Low: 49152, High: 57343}

// FullConfig holds the TWAMP-Control side of a Full run. Zero values take
// the defaults; the test packets are described by Config, whose Target is
// set from Accept-Session.
type FullConfig struct {
	// Server is the TWAMP-Control server. Port 0 means 862.
	Server netip.AddrPort
	// ControlTimeout bounds connecting and every control exchange.
	ControlTimeout time.Duration
	// ControlDSCP is the DSCP of the control connection, 0–63 (CMP-16).
	ControlDSCP int
	// SenderPort fixes the Sender Port (CMP-14); 0 picks a free one from
	// SenderPorts.
	SenderPort uint16
	// SenderPorts is the range the Sender Port is picked from (CMP-21).
	SenderPorts PortRange
	// ReceiverPort is the port asked of the reflector; 0 asks for the Sender
	// Port (CMP-03).
	ReceiverPort uint16
	// ZeroAddresses sends zero Sender and Receiver Addresses (CMP-01).
	ZeroAddresses bool
	// StartTimeNow sends the current time as Start Time instead of 0 (CMP-02).
	StartTimeNow bool
	// RejectAlternatePort fails the run when the server offers another
	// Receiver Port (accept_alternate_port: false, CMP-04).
	RejectAlternatePort bool
	// EndDelay is the wait between the end of the run and Stop-Sessions
	// (CMP-18).
	EndDelay time.Duration
	deps     fullDeps
	// Accepted, when set, is called once the server has accepted the
	// session, before Start-Sessions, with its SID and the reflector address
	// and port the test packets go to.
	Accepted func(sid wire.SID, reflector netip.AddrPort)
	// Started, when set, is called once Start-Ack has accepted the start,
	// just before the first test packet: the end of the control setup.
	Started func()
	// Modes are the security modes accepted, of which the strongest the
	// server offers is used (RFC 4656 §3.1, RFC 5618); zero means
	// unauthenticated only.
	Modes wire.Modes
	// KeyID and Passphrase are the shared secret of the authenticated,
	// encrypted and mixed modes.
	KeyID      string
	Passphrase []byte
	// Features are the TWAMP extensions to use where the server offers
	// them, of client.FeatureModes: under Individual Session Control the
	// session starts and stops by SID (RFC 5938); Symmetrical Size and
	// Reflect Octets shape the test packets (RFC 6038).
	Features wire.Modes
	// OctetsToReflect and PaddingToReflect are the Reflect Octets fields of
	// the request (RFC 6038 §4.2); they need that mode. The replies are
	// checked for the reflected padding (Summary.ReflectedMismatch).
	OctetsToReflect  uint16
	PaddingToReflect uint16
}

// fullDeps contains private transport injection for the daemon and tests.
type fullDeps struct {
	// Dial opens the control connection from local (invalid: any address);
	// nil dials TCP with keepalive and ControlDSCP.
	Dial func(ctx context.Context, local netip.Addr, server netip.AddrPort) (net.Conn, error)
	// Listen opens the Sender socket; nil means tstamp.Listen.
	Listen func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error)
}

// WithFullDial injects the control dialer for internal consumers.
func WithFullDial(fc FullConfig, dial func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error)) FullConfig {
	fc.deps.Dial = dial
	return fc
}

// WithFullListen injects the UDP listener for internal consumers.
func WithFullListen(fc FullConfig, listen func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error)) FullConfig {
	fc.deps.Listen = listen
	return fc
}

// WithDefaults returns fc with zero values replaced by the defaults.
func (fc FullConfig) WithDefaults() FullConfig {
	if fc.Server.Port() == 0 {
		fc.Server = netip.AddrPortFrom(fc.Server.Addr(), defaultControlPort)
	}
	fc.Server = netip.AddrPortFrom(fc.Server.Addr().Unmap(), fc.Server.Port())
	if fc.ControlTimeout <= 0 {
		fc.ControlTimeout = defaultControlTimeout
	}
	if fc.SenderPorts == (PortRange{}) {
		fc.SenderPorts = defaultSenderPorts
	}
	if fc.deps.Listen == nil {
		fc.deps.Listen = tstamp.Listen
	}
	return fc
}

// Validate checks the control configuration without opening a connection.
func (fc FullConfig) Validate() error {
	fc = fc.WithDefaults()
	switch {
	case !fc.Server.Addr().IsValid():
		return errors.New("sender: control server address is required")
	case fc.ControlDSCP < 0 || fc.ControlDSCP > 63:
		return fmt.Errorf("sender: control dscp %d out of range 0-63", fc.ControlDSCP)
	case fc.SenderPorts.Low == 0 || fc.SenderPorts.Low > fc.SenderPorts.High:
		return fmt.Errorf("sender: sender port range %d-%d is empty or starts at 0", fc.SenderPorts.Low, fc.SenderPorts.High)
	case fc.EndDelay < 0:
		return fmt.Errorf("sender: end delay %v is negative", fc.EndDelay)
	}
	return (client.Config{Modes: fc.Modes, KeyID: fc.KeyID, Passphrase: fc.Passphrase, Features: fc.Features}).Validate()
}

// ValidateFull checks both configurations, including security and socket options,
// without dialing or binding. Target is negotiated and is ignored.
func ValidateFull(cfg Config, fc FullConfig) error {
	fc = fc.WithDefaults()
	if err := fc.Validate(); err != nil {
		return err
	}
	cfg.Target = fc.Server
	return normalize(&cfg, true)
}

// RunFull carries out one TWAMP Full run on its own control connection
// (SPEC §5.6): it connects, binds the Sender Port, negotiates the session,
// starts it, runs the Session-Sender as Run does, and sends Stop-Sessions,
// also when ctx is canceled (SPEC §4.4). cfg.Target is ignored: test
// packets go to the address and port of Accept-Session.
//
// Errors of the control protocol, including a failed connect, are
// *client.Error, whose Stage and Accept tell what failed (SPEC §10.4 exit
// code 4). A canceled ctx returns its error with the aborted summary. When
// the control connection is lost during the run, the summary comes with an
// error matching client.ErrControlLost.
func RunFull(ctx context.Context, cfg Config, fc FullConfig, opts ...Option) (Summary, error) {
	fc = fc.WithDefaults()
	if err := fc.Validate(); err != nil {
		return Summary{}, err
	}
	check := cfg
	check.Target = fc.Server
	if err := normalize(&check, true); err != nil {
		return Summary{}, err
	}
	cfg = check

	tcp, err := fc.dial(ctx, cfg.Source.Addr())
	if err != nil {
		return Summary{}, &client.Error{Stage: client.StageConnect, Err: err}
	}
	c, err := client.Handshake(ctx, tcp, client.Config{
		Timeout: fc.ControlTimeout, RejectAlternatePort: fc.RejectAlternatePort,
		Modes: fc.Modes, KeyID: fc.KeyID, Passphrase: fc.Passphrase, Features: fc.Features,
	})
	if err != nil {
		return Summary{}, err
	}
	defer c.Close()

	local := addrOf(tcp.LocalAddr())
	conn, err := fc.bindSender(local, cfg)
	if err != nil {
		return Summary{}, err
	}
	defer conn.Close()

	req := client.Request{
		SenderPort:       conn.LocalAddr().Port(),
		ReceiverPort:     fc.ReceiverPort,
		ZeroAddresses:    fc.ZeroAddresses,
		PaddingLength:    uint32(cfg.Padding), //nolint:gosec // 0..MaxPadding, checked by normalize
		Timeout:          cfg.Timeout,
		DSCP:             uint8(cfg.DSCP), //nolint:gosec // 0..63, checked by normalize
		OctetsToReflect:  fc.OctetsToReflect,
		PaddingToReflect: fc.PaddingToReflect,
	}
	if fc.StartTimeNow {
		req.StartTime = time.Now()
	}
	sess, err := c.Request(ctx, req)
	if err != nil {
		return Summary{}, err
	}
	if fc.Accepted != nil {
		fc.Accepted(sess.SID, sess.Reflector)
	}
	cfg.Target = sess.Reflector
	cfg.Source = netip.AddrPort{}
	// Results collected when the control connection was lost are kept but
	// marked invalid (SPEC §5.6), in the summaries the hooks get too, which
	// also carry the SID.
	var o options
	for _, f := range opts {
		f(&o)
	}
	if h := o.hooks; h.Summary != nil {
		report := h.Summary
		h.Summary = func(s Summary) {
			s.SID = sess.SID
			s.Invalid = s.Invalid || c.Err() != nil
			report(s)
		}
		opts = append(opts, WithHooks(h))
	}
	snd, err := New(cfg, append(opts, WithConn(conn), withFull(), withSecurity(sess.Security, sess.TestKeys), withLayout(sess.Layout, sess.ServerOctets))...)
	if err != nil {
		return Summary{}, err
	}
	if err := start(ctx, c, sess); err != nil {
		return Summary{}, err
	}
	if fc.Started != nil {
		fc.Started()
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-c.Lost():
			cancel(c.Err())
		case <-runCtx.Done():
		}
	}()
	sum, runErr := snd.Run(runCtx)
	sum.SID = sess.SID
	if lost := c.Err(); lost != nil {
		// No Stop-Sessions is possible; the results may be invalid (SPEC §5.6).
		sum.Invalid = true
		return sum, lost
	}
	if runErr == nil && fc.EndDelay > 0 {
		select {
		case <-time.After(fc.EndDelay):
		case <-ctx.Done():
		}
	}
	accept := wire.AcceptOK
	if runErr != nil && ctx.Err() == nil {
		accept = wire.AcceptFailure // a socket failure: the results are invalid
	}
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), stopTimeout)
	defer stopCancel()
	if err := stop(stopCtx, c, sess, accept); err != nil && runErr == nil {
		return sum, err
	}
	return sum, runErr
}

// start starts the session: with Start-N-Sessions under Individual Session
// Control (RFC 5938), else with Start-Sessions.
func start(ctx context.Context, c *client.Client, sess *client.Session) error {
	if c.Features().Has(wire.ModeIndividualSessionControl) {
		_, err := c.StartN(ctx, []*client.Session{sess})
		return err
	}
	return c.Start(ctx)
}

// stop stops the session: with Stop-N-Sessions under Individual Session
// Control, which has no Accept to mark the results invalid, else with
// Stop-Sessions.
func stop(ctx context.Context, c *client.Client, sess *client.Session, accept wire.Accept) error {
	if c.Features().Has(wire.ModeIndividualSessionControl) {
		_, err := c.StopN(ctx, []*client.Session{sess})
		return err
	}
	return c.Stop(ctx, accept)
}

// dial opens the control connection, from local when it is valid.
func (fc FullConfig) dial(ctx context.Context, local netip.Addr) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, fc.ControlTimeout)
	defer cancel()
	if fc.deps.Dial != nil {
		return fc.deps.Dial(ctx, local, fc.Server)
	}
	d := net.Dialer{
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: keepAliveIdle, Interval: keepAliveInterval, Count: keepAliveCount},
	}
	if local.IsValid() {
		d.LocalAddr = net.TCPAddrFromAddrPort(netip.AddrPortFrom(local, 0))
	}
	if fc.ControlDSCP != 0 {
		tos := fc.ControlDSCP << 2
		v6 := !fc.Server.Addr().Is4()
		d.Control = func(_, _ string, rc syscall.RawConn) error { return setTOS(rc, tos, v6) }
	}
	return d.DialContext(ctx, "tcp", fc.Server.String())
}

// bindSender binds the Sender Port on the local address of the control
// connection: SenderPort when fixed, else a free port of SenderPorts scanned
// from a random start.
func (fc FullConfig) bindSender(local netip.Addr, cfg Config) (tstamp.Conn, error) {
	network := "udp6"
	if local.Is4() {
		network = "udp4"
	}
	opts := tstamp.Options{
		TTL: cfg.TTL, TOS: cfg.DSCP << 2, Mode: cfg.Timestamping, ReadBuffer: cfg.ReadBuffer,
		TxTimestamps: cfg.Timestamping != tstamp.ModeUserspace, // T1' (SPEC §8.2)
	}
	if fc.SenderPort != 0 {
		conn, err := fc.deps.Listen(network, netip.AddrPortFrom(local, fc.SenderPort), opts)
		if err != nil {
			return nil, fmt.Errorf("sender: bind sender port %d: %w", fc.SenderPort, err)
		}
		return conn, nil
	}
	r := fc.SenderPorts
	size := int(r.High) - int(r.Low) + 1
	start := mrand.IntN(size) //nolint:gosec // port choice, not a secret
	var err error
	for i := range size {
		port := uint16(int(r.Low) + (start+i)%size) //nolint:gosec // within r by construction
		var conn tstamp.Conn
		if conn, err = fc.deps.Listen(network, netip.AddrPortFrom(local, port), opts); err == nil {
			return conn, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, syscall.EACCES) {
			break
		}
	}
	return nil, fmt.Errorf("sender: bind a sender port in %d-%d: %w", r.Low, r.High, err)
}

// addrOf returns the unmapped IP address of a TCP endpoint, or the invalid
// address for other kinds of endpoints.
func addrOf(a net.Addr) netip.Addr {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort().Addr().Unmap()
	}
	ap, _ := netip.ParseAddrPort(a.String())
	return ap.Addr().Unmap()
}
