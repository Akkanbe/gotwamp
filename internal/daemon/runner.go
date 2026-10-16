package daemon

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// startFunc is told where a run sends from and to once that is known: at
// once for Light, after Accept-Session for Full. sid is nil for Light.
type startFunc func(local, remote netip.AddrPort, sid []byte)

// runner carries out the runs of ad-hoc tests and monitoring sessions
// (schedule.Runner), in TWAMP Light or Full.
//
//declscope:package // Start builds it; RunTest and the scheduler run through it
type runner struct {
	// resolve looks host names up; tests replace it.
	resolve func(ctx context.Context, network, host string) ([]netip.Addr, error)
	//declscope:private
	clock func() (bool, time.Duration)
	// sids holds the SID of the run in progress of each Full session.
	//
	//declscope:private
	sids *sidSink
	// controlSetup observes the time from connecting to Accept-Session of a
	// Full session run; it may be nil.
	//
	//declscope:private
	controlSetup func(*store.SessionInfo, time.Duration)
	// open opens the test sockets; nil opens them with tstamp.Listen.
	open *socketOpener
}

// newRunner returns a runner.
//
//declscope:package // Start builds it
func newRunner(clock func() (bool, time.Duration), sids *sidSink, controlSetup func(*store.SessionInfo, time.Duration)) *runner {
	return &runner{clock: clock, resolve: net.DefaultResolver.LookupNetIP, sids: sids, controlSetup: controlSetup}
}

// Run implements schedule.Runner for monitoring sessions.
func (r *runner) Run(ctx context.Context, spec *schedule.SessionSpec, hooks sender.Hooks) (sender.Summary, error) {
	opts, ok := spec.Options.(sessionOptions)
	if !ok {
		return sender.Summary{}, fmt.Errorf("session %s has no plan", spec.Name)
	}
	return r.run(ctx, &opts.plan, spec.Test, hooks, nil, spec)
}

// run carries out one run of p with the test parameters cfg (the plan's,
// or the scheduler's for a continuous session). Control protocol failures
// come back as *schedule.StageError. session is nil for ad-hoc tests.
//
//declscope:package // RunTest runs ad-hoc tests through it
func (r *runner) run(ctx context.Context, p *testPlan, cfg sender.Config, hooks sender.Hooks, started startFunc, session *schedule.SessionSpec) (sender.Summary, error) {
	if started == nil {
		started = func(netip.AddrPort, netip.AddrPort, []byte) {}
	}
	addr := p.addr
	if !addr.IsValid() {
		var err error
		if addr, err = r.target(ctx, p.host, p.ipv, p.source); err != nil {
			return sender.Summary{}, &schedule.StageError{Stage: "connect", Err: err}
		}
	}
	opts := []sender.Option{sender.WithHooks(hooks)}
	if r.clock != nil {
		opts = append(opts, sender.WithClock(r.clock))
	}
	if p.full {
		return r.runFull(ctx, p, cfg, addr, opts, started, session)
	}

	target := netip.AddrPortFrom(addr, p.receiverPort)
	// Light has no control exchange: a socket that cannot be opened is a
	// local failure of the test stage, not a protocol error (B6).
	conn, err := r.openLight(p, target, cfg)
	if err != nil {
		return sender.Summary{}, &schedule.StageError{Stage: "test", Err: err}
	}
	defer conn.Close()
	cfg.Target = target
	snd, err := sender.New(cfg, append(opts, sender.WithConn(conn))...)
	if err != nil {
		return sender.Summary{}, &schedule.StageError{Stage: "test", Err: err}
	}
	defer snd.Close()
	started(displayLocal(conn.LocalAddr(), target), target, nil)
	return snd.Run(ctx)
}

func (r *runner) runFull(ctx context.Context, p *testPlan, cfg sender.Config, addr netip.Addr, opts []sender.Option, started startFunc, session *schedule.SessionSpec) (sender.Summary, error) {
	fc := p.fc
	fc.Server = netip.AddrPortFrom(addr, p.controlPort)
	if p.source.IsValid() {
		cfg.Source = netip.AddrPortFrom(p.source, 0)
	}
	var local netip.AddrPort
	fc = sender.WithFullListen(fc, func(network string, l netip.AddrPort, o tstamp.Options) (tstamp.Conn, error) {
		c, err := r.open.listen(network, l, o)
		if err == nil {
			local = c.LocalAddr()
		}
		return c, err
	})
	begin := time.Now()
	name := ""
	if session != nil {
		name = session.Name
	}
	fc.Accepted = func(sid wire.SID, reflector netip.AddrPort) {
		r.sids.set(name, sid[:])
		started(local, reflector, sid[:])
	}
	// The control setup ends at Start-Ack (gotwamp_sender_control_setup_seconds).
	fc.Started = func() {
		if session != nil && r.controlSetup != nil {
			r.controlSetup(&session.Info, time.Since(begin))
		}
	}
	// A run starts without a SID, so that the events of a run that fails
	// before Accept-Session carry none. The SID stays after RunFull returns:
	// the scheduler publishes a periodic run's summary only then.
	r.sids.set(name, nil)
	sum, err := sender.RunFull(ctx, cfg, fc, opts...)
	var ce *client.Error
	if errors.As(err, &ce) {
		// The stage is a field of its own; the cause keeps the sentinels.
		err = &schedule.StageError{Stage: ce.Stage.String(), Accept: int(ce.Accept), Err: ce.Err}
	}
	return sum, err
}

// target resolves host in the address family ipv asks for, or that of
// source.
//
//declscope:package // RunTest resolves ad-hoc targets before it starts
func (r *runner) target(ctx context.Context, host, ipv string, source netip.Addr) (netip.Addr, error) {
	network := "ip"
	switch {
	case ipv == "4":
		network = "ip4"
	case ipv == "6":
		network = "ip6"
	case source.IsValid() && source.Is4():
		network = "ip4"
	case source.IsValid():
		network = "ip6"
	}
	addrs, err := r.resolve(ctx, network, host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	for _, a := range addrs {
		a = a.Unmap()
		if (network == "ip4" && !a.Is4()) || (network == "ip6" && a.Is4()) {
			continue
		}
		if source.IsValid() && source.Is4() != a.Is4() {
			continue
		}
		return a, nil
	}
	return netip.Addr{}, fmt.Errorf("%q has no %s address", host, map[string]string{"ip": "usable", "ip4": "IPv4", "ip6": "IPv6"}[network])
}

// openLight binds the Sender socket of a Light run: on the configured
// source address or the wildcard, at the fixed Sender Port or a free one of
// the range scanned from a random start.
func (r *runner) openLight(p *testPlan, target netip.AddrPort, cfg sender.Config) (tstamp.Conn, error) {
	network, src := "udp6", netip.IPv6Unspecified()
	if target.Addr().Is4() {
		network, src = "udp4", netip.IPv4Unspecified()
	}
	if p.source.IsValid() {
		src = p.source
	}
	opts := tstamp.Options{
		TTL: cfg.TTL, TOS: cfg.DSCP << 2, Mode: cfg.Timestamping, ReadBuffer: cfg.ReadBuffer,
		TxTimestamps: cfg.Timestamping != tstamp.ModeUserspace, // T1' (SPEC §8.2)
	}
	if p.senderPort != 0 {
		return r.open.listenTo(network, netip.AddrPortFrom(src, p.senderPort), opts, target.Addr())
	}
	lo, hi := int(p.senderPorts.First), int(p.senderPorts.Last)
	size := hi - lo + 1
	start := mrand.IntN(size) //nolint:gosec // G404: a port choice, not a secret
	var err error
	for i := range size {
		port := uint16(lo + (start+i)%size) //nolint:gosec // G115: within the range by construction
		var c tstamp.Conn
		if c, err = r.open.listenTo(network, netip.AddrPortFrom(src, port), opts, target.Addr()); err == nil {
			return c, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, syscall.EACCES) {
			break
		}
	}
	return nil, fmt.Errorf("no usable sender port in %d-%d: %w", lo, hi, err)
}

// displayLocal replaces a wildcard local address with the source address
// the route to target uses, which is what the reflector sees.
func displayLocal(local, target netip.AddrPort) netip.AddrPort {
	if !local.Addr().IsUnspecified() {
		return local
	}
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(target))
	if err != nil {
		return local
	}
	defer c.Close()
	src := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	return netip.AddrPortFrom(src, local.Port())
}

// sidSink fills in the SID of the run in progress of each Full session in
// the events it passes on, which the scheduler publishes without one.
type sidSink struct {
	next store.ResultSink
	mu   sync.Mutex
	sids map[string][]byte
}

// newSIDSink returns a sidSink passing events to next.
//
//declscope:package // Start puts it in front of the result sinks
func newSIDSink(next store.ResultSink) *sidSink {
	return &sidSink{next: next, sids: make(map[string][]byte)}
}

// set records the SID of session's current run; nil forgets it. Ad-hoc
// runs (empty name) are not recorded.
//
//declscope:package // a deleted session's SID is forgotten in Start's drop
func (s *sidSink) set(session string, sid []byte) {
	if session == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sid == nil {
		delete(s.sids, session)
		return
	}
	s.sids[session] = sid
}

// Publish implements store.ResultSink.
func (s *sidSink) Publish(e store.Event) {
	if e.SID == nil && e.Session != nil {
		s.mu.Lock()
		e.SID = s.sids[e.Session.Name]
		s.mu.Unlock()
	}
	s.next.Publish(e)
}
