//declscope:namespace light

// Package reflector implements the Session-Reflector of gotwampd.
//
// The Light reflector (SPEC §6.6) answers TWAMP-Test packets on preconfigured
// UDP listeners without TWAMP-Control. Reflecting one packet (SPEC §6.4) is
// split into the part every reflector shares, in core.go, and what is
// particular to Light: the ACL, the per-source rate limit and the flow table
// that numbers the replies of each 4-tuple.
package reflector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Sweep interval bounds for expiring flows and rate-limit buckets.
const (
	minSweep = time.Second
	maxSweep = time.Minute
)

// Deps are the collaborators of the Light reflector.
type Deps struct {
	// ListenControl opens a TCP control listener; nil uses ListenControl.
	ListenControl func(network string, local netip.AddrPort) (net.Listener, error)
	// ControlLogger receives control server messages; nil uses Logger.
	ControlLogger *slog.Logger

	// Listen opens a UDP socket; nil means tstamp.Listen.
	Listen func(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error)
	// Clock reports the clock state for the Error Estimate; nil sends
	// errest.Unknown.
	Clock ClockState
	// Logger receives operational messages; nil discards them.
	Logger *slog.Logger
	// Hook takes part in every Light reply, for STAMP (SPEC §13); nil
	// means none. The Full reflector does not use it.
	//
	//declscope:ignore overexported // the STAMP extension point of SPEC §13, exercised by the tests
	Hook Hook
}

// ListenerStatus is the state of one Light listener.
type ListenerStatus struct {
	// Address is the bound address and port.
	Address netip.AddrPort
	// Timestamping is the receive timestamping in effect.
	Timestamping tstamp.Mode
	// Counters are the packet counters of the listener.
	Counters Counters
}

// Status is a snapshot of the Light reflector.
type Status struct {
	// Listeners are in configuration order.
	Listeners []ListenerStatus
	// Counters sum the counters of every listener.
	Counters Counters
	// FlowsActive is the number of flows with state.
	FlowsActive int
	// FlowsOverflow counts packets of new flows handled statelessly
	// because the flow table was full.
	FlowsOverflow uint64
}

// Light is a TWAMP Light reflector.
type Light struct {
	cfg       Config
	clock     ClockState
	log       *slog.Logger
	listeners []*listener
	flows     *flowTable
	limiter   *rateLimiter // nil when unlimited
	acl       []netip.Prefix
	hook      Hook

	life serviceLife
}

// listener is one bound socket and its counters.
type listener struct {
	conn     tstamp.Conn
	addr     netip.AddrPort
	counters counters
}

// ListenLight validates cfg, applying its defaults, and binds every listener.
// Call Serve to start reflecting, or Close to release the sockets.
func ListenLight(cfg Config, deps Deps) (*Light, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	listen := deps.Listen
	if listen == nil {
		listen = tstamp.Listen
	}
	l := &Light{
		cfg:   cfg,
		clock: deps.Clock,
		log:   deps.Logger,
		flows: newFlowTable(cfg.FlowTimeout, cfg.MaxFlows),
		acl:   make([]netip.Prefix, len(cfg.ACL)),
		hook:  deps.Hook,
	}
	if l.log == nil {
		l.log = slog.New(slog.DiscardHandler)
	}
	if cfg.RateLimitPPS > 0 {
		l.limiter = newRateLimiter(cfg.RateLimitPPS)
	}
	for i, p := range cfg.ACL {
		l.acl[i] = unmapPrefix(p)
	}
	opts := tstamp.Options{TTL: cfg.TTL, Mode: cfg.Timestamping, ReadBuffer: cfg.ReadBuffer}
	if cfg.DSCPPolicy == DSCPFixed {
		opts.TOS = int(cfg.DSCPFixed) << 2
	}
	for _, addr := range cfg.Listeners {
		network := "udp6"
		if addr.Addr().Unmap().Is4() {
			network = "udp4"
			addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		}
		conn, err := listen(network, addr, opts)
		if err != nil {
			err = fmt.Errorf("light reflector: listen %v: %w", addr, err)
			return nil, errors.Join(err, l.Close())
		}
		l.listeners = append(l.listeners, &listener{conn: conn, addr: conn.LocalAddr()})
	}
	return l, nil
}

// unmapPrefix turns an IPv4-mapped IPv6 prefix into the IPv4 prefix it
// stands for, so that it matches the unmapped source addresses.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if !p.Addr().Is4In6() || p.Bits() < 96 {
		return p.Masked()
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96).Masked()
}

// Addrs returns the bound address of every listener, in configuration order.
func (l *Light) Addrs() []netip.AddrPort {
	out := make([]netip.AddrPort, len(l.listeners))
	for i, ln := range l.listeners {
		out[i] = ln.addr
	}
	return out
}

// Serve reflects packets until ctx is done or Close is called. It may be
// called once; after Close it returns an error without starting workers.
func (l *Light) Serve(ctx context.Context) error {
	ctx, finish, err := l.life.begin(ctx)
	if err != nil {
		return fmt.Errorf("light reflector: %w", err)
	}
	defer finish()
	var wg sync.WaitGroup
	for _, ln := range l.listeners {
		wg.Go(func() { l.serveListener(ln) })
	}
	wg.Go(func() { l.sweep(ctx) })
	<-ctx.Done()
	err = l.life.shutdown(l.closeSockets)
	wg.Wait()
	return err
}

// Close closes every listener and waits for all serving workers to stop.
// It is safe before Serve and may be called concurrently or repeatedly.
func (l *Light) Close() error {
	err := l.life.shutdown(l.closeSockets)
	l.life.wait()
	return err
}

func (l *Light) closeSockets() error {
	var errs []error
	for _, ln := range l.listeners {
		if err := ln.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("light reflector: close %v: %w", ln.addr, err))
		}
	}
	return errors.Join(errs...)
}

// Status returns the counters and flow counts.
func (l *Light) Status() Status {
	var s Status
	for _, ln := range l.listeners {
		c := ln.counters.snapshot()
		s.Listeners = append(s.Listeners, ListenerStatus{Address: ln.addr, Timestamping: ln.conn.Mode(), Counters: c})
		s.Counters.add(c)
	}
	s.FlowsActive, s.FlowsOverflow = l.flows.stats()
	return s
}

// Flows returns a snapshot of the flows that have not expired, unordered.
func (l *Light) Flows() []Flow {
	return l.flows.list(time.Now())
}

// sweep forgets idle flows and rate-limit buckets until ctx is done.
func (l *Light) sweep(ctx context.Context) {
	t := time.NewTicker(min(max(l.cfg.FlowTimeout/10, minSweep), maxSweep))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.flows.expire(now)
			if l.limiter != nil {
				l.limiter.sweep(now)
			}
		}
	}
}

// serveListener reads and reflects packets of one listener until its socket
// is closed.
func (l *Light) serveListener(ln *listener) {
	// One octet more than the largest accepted packet detects longer ones;
	// the buffer must also hold the longest reply header.
	buf := make([]byte, max(l.cfg.MaxPacketSize+1, wire.ReflectorDSCPECNHeaderSize))
	est := estimator{clock: l.clock, methodError: reflectorMethodError(l.cfg.UserspaceError, l.cfg.KernelError)}
	errs := readErrors{log: l.log}
	for {
		n, from, rx, err := ln.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				errs.failed("light reflector: read failed", err, "listener", ln.addr)
			}
			continue
		}
		if rx.At.IsZero() {
			rx.At = time.Now()
		}
		l.handle(ln, &est, buf, n, from, rx)
	}
}

// handle reflects one received datagram (SPEC §6.4 steps 2–9).
func (l *Light) handle(ln *listener, est *estimator, buf []byte, n int, from netip.AddrPort, rx tstamp.RxMeta) {
	ln.counters.received.Add(1)
	src := from.Addr().Unmap()
	if !l.allowed(src) {
		ln.counters.dropped[DropACL].Add(1)
		return
	}
	if l.limiter != nil && !l.limiter.allow(src, rx.At) {
		ln.counters.dropped[DropRateLimit].Add(1)
		return
	}
	var sp wire.SenderPacket
	if n > l.cfg.MaxPacketSize || sp.Decode(buf[:n]) != nil {
		ln.counters.dropped[DropMalformed].Add(1)
		return
	}
	// local is where the packet arrived: the listener's address, or for a
	// listener on the wildcard the destination the socket reported. It keys
	// the flow and is the source of the reply (SPEC §6.1, §6.6).
	local, replyFrom := ln.addr, netip.Addr{}
	if ln.addr.Addr().IsUnspecified() && rx.Dst.IsValid() {
		replyFrom = rx.Dst.Unmap()
		local = netip.AddrPortFrom(replyFrom, ln.addr.Port())
	}
	var in *HookPacket // allocated only when a hook is installed
	if l.hook != nil {
		// The reply is built over the request: keep its head for the hook.
		in = &HookPacket{From: from, To: local, Len: n, RX: rx}
		in.Head = append(in.headBuf[:0], buf[:min(n, len(in.headBuf))]...)
	}
	seq, stateful := l.flows.receive(from, local, rx.At, sp.Seq, l.cfg.Sequence == SequenceCopySender)
	// Reflect cannot fail: n ≥ 14 was checked and buf holds any reply header.
	m, _ := buildReply(buf, n, seq, est.estimate(rx.At), rx, replyFormat{dscpecn: l.cfg.DSCPECNMonitoring, stamp: l.cfg.STAMP})
	if in != nil {
		var ok bool
		if m, ok = l.hook.Reply(in, buf[:m]); !ok || m < wire.SenderHeaderSize || m > len(buf) {
			ln.counters.dropped[DropMalformed].Add(1)
			return
		}
	}
	if err := sendReply(ln.conn, buf[:m], from, replyFrom, l.replyTOS(rx), nil); err != nil {
		ln.counters.sendErrors.Add(1)
		l.log.Debug("light reflector: send failed", "listener", ln.addr, "to", from, "err", err)
		return
	}
	ln.counters.reflected.Add(1)
	if stateful {
		l.flows.reflected(from, local)
	}
}

// allowed applies the ACL to an unmapped source address.
func (l *Light) allowed(src netip.Addr) bool {
	if len(l.acl) == 0 {
		return true
	}
	for _, p := range l.acl {
		if p.Contains(src) {
			return true
		}
	}
	return false
}

// replyTOS returns the per-packet TOS of a reply (SPEC §6.4 step 7). DSCP
// copy keeps the received DSCP and clears ECN to Not-ECT (SPEC §6.1); an
// unreadable TOS falls back to CS0. DSCP fixed leaves the socket's TOS.
func (l *Light) replyTOS(rx tstamp.RxMeta) int {
	if l.cfg.DSCPPolicy == DSCPFixed {
		return -1
	}
	if rx.TOS < 0 {
		return 0
	}
	return rx.TOS &^ 0x03
}
