package sender

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// maxExpirePeriod bounds how often the receiver looks for timed-out packets
// (SPEC §6.5: the shorter of interval and 100 ms).
const maxExpirePeriod = 100 * time.Millisecond

// maxReply is the receive buffer size. A reflector reply is never longer
// than the largest UDP payload.
const maxReply = 65535

// runParams is what a Sender hands over to its run.
//
//declscope:package // Sender.Run fills it
type runParams struct {
	// cfg is normalized.
	cfg  Config
	conn tstamp.Conn
	// packet is the Session-Sender packet with its padding filled.
	packet []byte
	// clock may be nil.
	clock func() (bool, time.Duration)
	hooks Hooks
	// fresh tells that the reflector numbers its reflections from 0: a new
	// TWAMP Full session.
	fresh bool
	// sec protects the test packets of the authenticated modes.
	sec testSecurity
	// reflected are the octets every reply must return (RFC 6038).
	reflected []byte
}

// runSession carries out one run of a Sender.
//
//declscope:package // Sender.Run hands its state over here
func runSession(ctx context.Context, p runParams) (Summary, error) {
	r := newRun(p.cfg, p.conn, p.packet, p.clock, p.hooks)
	r.fresh, r.reflected = p.fresh, p.reflected
	sec := p.sec
	if sec.mode != 0 {
		// The transmitter and the receiver run in two goroutines: one each.
		var err error
		if r.txCrypto, err = wire.NewTestCrypto(sec.mode, sec.keys); err != nil {
			return Summary{}, fmt.Errorf("sender: %w", err)
		}
		r.rxCrypto, _ = wire.NewTestCrypto(sec.mode, sec.keys)
	}
	return r.execute(ctx)
}

func newRun(cfg Config, conn tstamp.Conn, packet []byte, clock func() (bool, time.Duration), hooks Hooks) *run {
	r := &run{
		cfg:         cfg,
		conn:        conn,
		packet:      packet,
		clock:       clock,
		hooks:       hooks,
		perWindow:   cfg.Count,
		methodError: cfg.MethodError,
		rbuf:        make([]byte, maxReply+1),
	}
	if r.perWindow == 0 {
		r.perWindow = uint32(max(1, min(cfg.Window/cfg.Interval, 1<<31)))
	}
	if r.methodError == 0 {
		switch conn.Mode() {
		case tstamp.ModeHardware:
			r.methodError = cmp.Or(cfg.HardwareError, cfg.KernelError, defaultKernelError)
		case tstamp.ModeKernel:
			r.methodError = cmp.Or(cfg.KernelError, defaultKernelError)
		default:
			r.methodError = cmp.Or(cfg.UserspaceError, defaultUserspaceError)
		}
	}
	r.local = localAddr(conn, cfg.Target)
	wait := cmp.Or(cfg.TxTimestampWait, defaultTxTimestampWait)
	r.tx = newTxTracker(conn, max(wait, 0), int(min(cfg.Timeout/cfg.Interval, 1<<20))+64)
	return r
}

// window is the accumulator of a run, or of one window of a continuous
// session: the packets with Sender Sequence Numbers first to
// first + perWindow − 1.
type window struct {
	index   int
	first   uint32
	started time.Time
	acc     *stats.Accumulator
	// sendDone is set once the last packet of the window is sent. Until
	// then the transmitter calls acc too, under run.mu.
	sendDone bool
	// The counters are used by the receiver only.
	malformed, foreign   uint64
	reflectedMismatch    uint64
	txMissing, txInvalid uint64
}

// run is the state of one Run. The transmitter and the receiver share the
// open windows under mu; everything else belongs to one of them.
type run struct {
	cfg         Config
	conn        tstamp.Conn
	hooks       Hooks
	clock       func() (bool, time.Duration)
	methodError time.Duration
	// packet is the Session-Sender packet, owned by the transmitter.
	packet    []byte
	perWindow uint32
	// tx pairs packets with their kernel transmit timestamps; nil when the
	// socket gives none.
	tx *txTracker
	// local is the address the packets leave from, as reported in Summary.
	local netip.AddrPort
	// seqBase is the Sender Sequence Number of the first packet; 0 but in
	// tests of the wrap-around.
	seqBase uint32
	// fresh tells that the reflector numbers the reflections of the run
	// from 0, which the first window then knows as its base (SPEC §7.2).
	fresh bool
	// txCrypto and rxCrypto protect the test packets of the authenticated
	// and encrypted modes, for the transmitter and the receiver; nil when
	// unauthenticated.
	txCrypto, rxCrypto *wire.TestCrypto
	// reflected are the octets each reply must return at the start of its
	// padding (RFC 6038 Reflect Octets); nil when none.
	reflected []byte

	mu       sync.Mutex
	windows  []*window
	sendDone bool

	// Receiver state, reused to keep the receive path free of allocation.
	rbuf       []byte
	lost       []stats.Lost
	lostEvents []LostPacket
	done       []*window
	last       Summary
}

func (r *run) execute(ctx context.Context) (Summary, error) {
	t0 := time.Now()
	first := r.newWindow(0, r.seqBase, t0)
	if r.fresh {
		first.acc.SetReflectorBase(0)
	}
	r.windows = append(r.windows, first)

	// runCtx also ends the run when the transmitter panics, which fails the
	// run instead of the process (SPEC §12.2).
	runCtx, stopRun := context.WithCancelCause(ctx)
	defer stopRun(nil)
	sendCtx, stopSending := context.WithCancel(runCtx)
	defer stopSending()
	var wg sync.WaitGroup
	wg.Go(func() {
		defer func() {
			if v := recover(); v != nil {
				stopRun(fmt.Errorf("sender: transmit panicked: %v", v))
			}
		}()
		r.transmit(sendCtx, t0)
	})
	// Wake the receiver as soon as the run is canceled.
	stopWaking := context.AfterFunc(runCtx, func() { _ = r.conn.SetReadDeadline(time.Now()) })
	defer stopWaking()

	err := r.receive(runCtx, t0)
	stopSending()
	wg.Wait()
	_ = r.conn.SetReadDeadline(time.Time{})

	r.mu.Lock()
	open := r.windows
	r.windows = nil
	r.mu.Unlock()
	now := time.Now()
	for _, w := range open {
		r.emit(w, now, true)
	}
	if err == nil && runCtx.Err() != nil {
		err = context.Cause(runCtx)
	}
	return r.last, err
}

// localAddr returns the address and port conn sends to target from. A
// socket bound to the wildcard address takes its source address from the
// route, which a UDP socket connected to target reveals without sending
// anything (SPEC §7.5: the actual addresses).
func localAddr(conn tstamp.Conn, target netip.AddrPort) netip.AddrPort {
	la := conn.LocalAddr()
	if !la.Addr().IsUnspecified() || !target.IsValid() {
		return la
	}
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(target))
	if err != nil {
		return la
	}
	defer c.Close()
	src := c.LocalAddr().(*net.UDPAddr).AddrPort().Addr()
	if la.Addr().Is4() {
		src = src.Unmap()
	}
	return netip.AddrPortFrom(src, la.Port())
}

func (r *run) newWindow(index int, first uint32, started time.Time) *window {
	ttl := uint8(255) // normalize keeps TTL within 1–255
	if r.cfg.TTL > 0 && r.cfg.TTL < 255 {
		ttl = uint8(r.cfg.TTL)
	}
	return &window{
		index:   index,
		first:   first,
		started: started,
		acc: stats.NewAccumulator(stats.Config{
			Timeout:            r.cfg.Timeout,
			MaxSamples:         r.cfg.MaxSamples,
			SendTTL:            ttl,
			StatelessReflector: r.cfg.StatelessReflector,
			SendDSCP:           uint8(r.cfg.DSCP), //nolint:gosec // G115: normalize keeps DSCP within 0–63
		}),
	}
}

// transmit sends packet i at t0 + i × interval (SPEC §6.5), opening a new
// window every perWindow packets. Only a run that sent every packet marks
// its last window done.
func (r *run) transmit(ctx context.Context, t0 time.Time) {
	if r.cfg.LockOSThread {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	r.mu.Lock()
	cur := r.windows[0]
	r.mu.Unlock()
	for i := uint64(0); r.cfg.Count == 0 || i < uint64(r.cfg.Count); i++ {
		at := t0.Add(time.Duration(i) * r.cfg.Interval)
		if !r.wait(ctx, timer, at) {
			// Canceled: the open windows are aborted, not completed.
			return
		}
		seq := r.seqBase + uint32(i) //nolint:gosec // G115: the sequence number wraps by design
		// A new window also starts where the sequence number wraps, so that
		// no window holds numbers from both ends (RFC 4737 compares them).
		if seq-cur.first >= r.perWindow || seq == 0 && i > 0 {
			next := r.newWindow(cur.index+1, seq, at)
			r.mu.Lock()
			cur.sendDone = true
			r.windows = append(r.windows, next)
			r.mu.Unlock()
			cur = next
		}
		r.send(cur, seq)
	}
	r.mu.Lock()
	cur.sendDone = true
	r.sendDone = true
	r.mu.Unlock()
}

// wait sleeps until SpinBefore ahead of at and spins for the rest (SPEC
// §8.6). It reports false when ctx is canceled first.
func (r *run) wait(ctx context.Context, timer *time.Timer, at time.Time) bool {
	spin := max(r.cfg.SpinBefore, 0)
	if d := time.Until(at); d > spin {
		timer.Reset(d - spin)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	for time.Now().Before(at) {
		if ctx.Err() != nil {
			return false
		}
		runtime.Gosched()
	}
	return ctx.Err() == nil
}

// send stamps and sends packet seq. The packet is recorded before it is
// handed to the socket, so that a reply can never arrive unannounced; a
// packet that cannot be sent is turned into a send error.
func (r *run) send(w *window, seq uint32) {
	b := r.packet
	ee := r.estimate()
	t1 := time.Now()
	r.stamp(b, seq, ee, t1)
	r.mu.Lock()
	w.acc.Sent(seq, t1)
	r.mu.Unlock()

	err := r.write(b, seq)
	if errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.EAGAIN) {
		t1 = time.Now()
		r.stamp(b, seq, ee, t1)
		r.mu.Lock()
		w.acc.Resent(seq, t1)
		r.mu.Unlock()
		err = r.write(b, seq)
	}
	if err != nil {
		r.mu.Lock()
		w.acc.Unsent(seq)
		r.mu.Unlock()
	}
	if r.tx != nil {
		// Collect T1' every iteration (SPEC §8.2), which also keeps the
		// error queue from filling the receive buffer.
		r.tx.drain()
	}
}

// stamp writes the header of packet seq with T1 into b, and seals it in the
// authenticated and encrypted modes, where T1 lies under the HMAC or the
// cipher. b always holds the header.
func (r *run) stamp(b []byte, seq uint32, ee errest.Estimate, t1 time.Time) {
	if r.cfg.STAMP {
		pkt := wire.STAMPSenderPacket{Seq: seq, Timestamp: ntptime.FromTime(t1), ErrorEstimate: ee, SSID: r.cfg.SSID}
		_ = pkt.PutHeader(b)
		return
	}
	if r.txCrypto == nil {
		pkt := wire.SenderPacket{Seq: seq, Timestamp: ntptime.FromTime(t1), ErrorEstimate: ee}
		_ = pkt.PutHeader(b)
		return
	}
	pkt := wire.AuthSenderPacket{Seq: seq, Timestamp: ntptime.FromTime(t1), ErrorEstimate: ee}
	_ = pkt.PutHeader(b)
	_ = r.txCrypto.SealSender(b)
}

// write hands packet seq to the socket, through the T1' tracker when there
// is one.
func (r *run) write(b []byte, seq uint32) error {
	if r.tx != nil {
		return r.tx.send(b, r.cfg.Target, seq)
	}
	_, err := r.conn.WriteTo(b, r.cfg.Target, -1)
	return err
}

// estimate returns the Error Estimate of the next packet (SPEC §8.4).
func (r *run) estimate() errest.Estimate {
	if r.clock == nil {
		return errest.Unknown
	}
	synced, maxErr := r.clock()
	return errest.Compute(maxErr+r.methodError, synced)
}

// receive reads replies until every window is summarized, the context is
// canceled, or the socket fails. Timed-out packets are retired every
// min(interval, 100 ms).
func (r *run) receive(ctx context.Context, t0 time.Time) error {
	if r.cfg.LockOSThread {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
	}
	period := min(r.cfg.Interval, maxExpirePeriod)
	next := t0.Add(period)
	var armed time.Time
	for ctx.Err() == nil && !r.finished() {
		if !next.Equal(armed) {
			if err := r.conn.SetReadDeadline(next); err != nil {
				return fmt.Errorf("sender: set read deadline: %w", err)
			}
			armed = next
		}
		n, from, meta, err := r.conn.ReadFrom(r.rbuf)
		if err != nil {
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("sender: receive: %w", err)
			}
			now := time.Now()
			r.expire(now)
			next = now.Add(period)
			continue
		}
		r.handle(n, from, meta)
		if !meta.At.Before(next) {
			r.expire(meta.At)
			next = meta.At.Add(period)
		}
	}
	return nil
}

func (r *run) finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sendDone && len(r.windows) == 0
}

// handle validates one datagram and passes it to the window of its Sender
// Sequence Number (SPEC §6.5 receive steps 2–5).
func (r *run) handle(n int, from netip.AddrPort, meta tstamp.RxMeta) {
	r.mu.Lock()
	if len(r.windows) == 0 {
		r.mu.Unlock()
		return
	}
	newest := r.windows[len(r.windows)-1]
	r.mu.Unlock()
	if netip.AddrPortFrom(from.Addr().Unmap(), from.Port()) != r.cfg.Target {
		newest.foreign++
		return
	}
	// A STAMP reply carrying another session's SSID is not ours (RFC 8972
	// §3); a TWAMP reflector returns zero there.
	if ssid := wire.STAMPSSID(r.rbuf[:n]); r.cfg.SSID != 0 && ssid != 0 && ssid != r.cfg.SSID {
		newest.foreign++
		return
	}
	p, ok := r.decodeReply(r.rbuf[:n])
	if !ok || r.cfg.DropZeroMultiplier && !p.ErrorEstimate.Valid() {
		newest.malformed++
		return
	}
	if r.reflected != nil && !bytes.HasPrefix(p.Padding, r.reflected) {
		newest.reflectedMismatch++
	}
	rec := stats.PacketRecord{
		SenderSeq:      p.SenderSeq,
		ReflectorSeq:   p.Seq,
		T1:             p.SenderTimestamp.Time(meta.At),
		T2:             p.ReceiveTimestamp.Time(meta.At),
		T3:             p.Timestamp.Time(meta.At),
		T4:             meta.At,
		SenderClock:    clockStatus(p.SenderErrorEstimate),
		ReflectorClock: clockStatus(p.ErrorEstimate),
		SenderTTL:      p.SenderTTL,
		DSCPECN:        uint8(p.DSCPECN),
		HasDSCPECN:     p.HasDSCPECN,
	}
	if meta.TTL > 0 && meta.TTL <= 255 {
		rec.ReplyTTL = uint8(meta.TTL)
	}
	outcome := txMissing
	if r.tx != nil {
		rec.T1, outcome = r.tx.t1(p.SenderSeq, rec.T1, rec.T4)
	}

	r.mu.Lock()
	w := r.windowOf(p.SenderSeq)
	res := w.acc.Add(rec)
	r.mu.Unlock()
	if r.tx != nil && res.Kind == stats.KindAccepted {
		switch outcome {
		case txMissing:
			w.txMissing++
		case txInvalid:
			w.txInvalid++
		}
	}
	if r.hooks.Packet != nil {
		r.hooks.Packet(PacketResult{
			Window:                 w.index,
			Record:                 rec,
			Result:                 res,
			SenderErrorEstimate:    p.SenderErrorEstimate,
			ReflectorErrorEstimate: p.ErrorEstimate,
			TOS:                    meta.TOS,
			T1FromKernel:           r.tx != nil && outcome == txKernel,
		})
	}
	if res.Kind == stats.KindAccepted || res.Kind == stats.KindLate {
		r.closeDecided(meta.At)
	}
}

// decodeReply decodes a reply, which in the authenticated and encrypted
// modes must first pass its HMAC (RFC 4656 §4.2: MUST be discarded
// otherwise). With DSCP and ECN monitoring the unauthenticated header is 44
// octets, and a shorter reply is malformed (RFC 7750 §2.1).
func (r *run) decodeReply(b []byte) (wire.ReflectorPacket, bool) {
	p := wire.ReflectorPacket{HasDSCPECN: r.cfg.DSCPECNMonitoring}
	if r.rxCrypto == nil {
		return p, len(b) >= wire.ReflectorHeaderSize && p.Decode(b) == nil
	}
	a := wire.AuthReflectorPacket{HasDSCPECN: r.cfg.DSCPECNMonitoring}
	if r.rxCrypto.OpenReflector(b) != nil || a.Decode(b) != nil {
		return p, false
	}
	p.Seq, p.Timestamp, p.ErrorEstimate, p.ReceiveTimestamp = a.Seq, a.Timestamp, a.ErrorEstimate, a.ReceiveTimestamp
	p.SenderSeq, p.SenderTimestamp, p.SenderErrorEstimate = a.SenderSeq, a.SenderTimestamp, a.SenderErrorEstimate
	p.SenderTTL, p.DSCPECN, p.Padding = a.SenderTTL, a.DSCPECN, a.Padding
	return p, true
}

// windowOf returns the open window that sent seq, or the newest one, whose
// accumulator then counts the reply as stray. r.mu must be held.
func (r *run) windowOf(seq uint32) *window {
	for i := len(r.windows) - 1; i >= 0; i-- {
		if w := r.windows[i]; seq-w.first < r.perWindow {
			return w
		}
	}
	return r.windows[len(r.windows)-1]
}

func clockStatus(e errest.Estimate) stats.ClockStatus {
	return stats.ClockStatus{
		Synced:            e.Synchronized(),
		Error:             e.Duration(),
		ErrorUnknown:      !e.Valid(),
		UnsupportedFormat: e.PTP(),
	}
}

// expire retires the packets whose timeout passed by now in every open
// window, reports the lost ones and summarizes the windows that are done.
func (r *run) expire(now time.Time) {
	r.lostEvents = r.lostEvents[:0]
	r.mu.Lock()
	for _, w := range r.windows {
		r.lost = w.acc.Expire(now, r.lost[:0])
		for _, l := range r.lost {
			r.lostEvents = append(r.lostEvents, LostPacket{Window: w.index, Seq: l.Seq, T1: l.T1})
		}
	}
	r.mu.Unlock()
	if r.hooks.Lost != nil {
		for _, l := range r.lostEvents {
			r.hooks.Lost(l)
		}
	}
	r.closeDecided(now)
}

// closeDecided summarizes and drops the windows whose packets are all sent
// and decided. The window the transmitter is filling is never one of them,
// so windows is empty only once the run is finished.
func (r *run) closeDecided(now time.Time) {
	r.done = r.done[:0]
	r.mu.Lock()
	open := r.windows[:0]
	for _, w := range r.windows {
		if w.sendDone && w.acc.Pending() == 0 {
			r.done = append(r.done, w)
		} else {
			open = append(open, w)
		}
	}
	clear(r.windows[len(open):])
	r.windows = open
	// The windows are parts of one reflected session: a window that saw
	// every one of its reflections tells the next one where the
	// reflector's numbers resume (SPEC §7.2, §7.5). After a window with a
	// loss that is unsure, and the next one is left without a base.
	for _, w := range r.done {
		if base, ok := w.acc.NextReflectorBase(); ok {
			for _, n := range r.windows {
				if n.index == w.index+1 {
					n.acc.SetReflectorBase(base)
				}
			}
		}
	}
	r.mu.Unlock()
	// The transmitter no longer touches a window once sendDone is set, so
	// the summaries can be built without holding the lock.
	for _, w := range r.done {
		r.emit(w, now, false)
	}
}

func (r *run) emit(w *window, now time.Time, aborted bool) {
	r.last = Summary{
		Summary:           w.acc.Summary(),
		Window:            w.index,
		StartedAt:         w.started,
		EndedAt:           now,
		Aborted:           aborted,
		Malformed:         w.malformed,
		ReflectedMismatch: w.reflectedMismatch,
		ForeignSource:     w.foreign,
		TxTimestamps:      r.tx != nil,
		TxMissing:         w.txMissing,
		TxInvalid:         w.txInvalid,
		Local:             r.local,
		Remote:            r.cfg.Target,
		Timestamping:      r.conn.Mode(),
	}
	if r.hooks.Summary != nil {
		r.hooks.Summary(r.last)
	}
}
