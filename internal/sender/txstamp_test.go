//declscope:namespace run // tests of the T1' bookkeeping, on the fake link of run_test.go

package sender

import (
	"net/netip"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// txBehavior is what the fake kernel does with the transmit timestamp of
// one packet.
type txBehavior struct {
	never  bool          // no timestamp at all
	after  time.Duration // queued this long after the send
	offset time.Duration // T1' minus the user-space send time
}

type queuedStamp struct {
	id        uint32
	at        time.Time
	available time.Time
}

// txLink is a link whose fake kernel also reports transmit timestamps.
type txLink struct {
	*link
	stampOf func(seq uint32) txBehavior
	enobufs map[uint32]bool // seqs whose first send fails with ENOBUFS

	nextID uint32
	queue  []queuedStamp
}

func newTxLink() *txLink {
	return &txLink{
		link:    newLink(),
		stampOf: func(uint32) txBehavior { return txBehavior{offset: 3 * time.Microsecond} },
		enobufs: map[uint32]bool{},
	}
}

func (l *txLink) TxTimestamps() bool { return true }

func (l *txLink) WriteToID(b []byte, to netip.AddrPort, tos int) (time.Time, uint32, error) {
	var p wire.SenderPacket
	_ = p.Decode(b)
	l.mu.Lock()
	id := l.nextID
	l.nextID++ // the kernel numbers a packet it then drops for ENOBUFS too
	fail := l.enobufs[p.Seq]
	delete(l.enobufs, p.Seq)
	l.mu.Unlock()
	if fail {
		return time.Now(), id, syscall.ENOBUFS
	}
	at, err := l.WriteTo(b, to, tos)
	if err != nil {
		return at, id, err
	}
	if bh := l.stampOf(p.Seq); !bh.never {
		l.mu.Lock()
		l.queue = append(l.queue, queuedStamp{id: id, at: at.Add(bh.offset), available: at.Add(bh.after)})
		l.mu.Unlock()
	}
	return at, id, nil
}

func (l *txLink) ReadTxTimestamps(fn func(uint32, time.Time)) (int, error) {
	now := time.Now()
	l.mu.Lock()
	var ready []queuedStamp
	kept := l.queue[:0]
	for _, q := range l.queue {
		if q.available.After(now) {
			kept = append(kept, q)
		} else {
			ready = append(ready, q)
		}
	}
	l.queue = kept
	l.mu.Unlock()
	for _, q := range ready {
		fn(q.id, q.at)
	}
	return len(ready), nil
}

func TestKernelT1(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTxLink()
		// Replies come back 11 ms after the send.
		l.stampOf = func(seq uint32) txBehavior {
			switch seq {
			case 1:
				return txBehavior{never: true}
			case 2:
				return txBehavior{after: 11500 * time.Microsecond, offset: 3 * time.Microsecond} // within the 1 ms wait
			case 3:
				return txBehavior{after: 13 * time.Millisecond, offset: 3 * time.Microsecond} // beyond it
			case 4:
				return txBehavior{offset: -200 * time.Microsecond} // before T1
			case 5:
				return txBehavior{offset: 20 * time.Millisecond} // after T4
			}
			return txBehavior{offset: 3 * time.Microsecond}
		}
		l.enobufs[6] = true
		t0 := time.Now()
		sum, rec, err := startRun(t.Context(), t, testConfig(8), l.link, WithConn(l))
		if err != nil {
			t.Fatal(err)
		}
		if !sum.TxTimestamps || sum.TxMissing != 2 || sum.TxInvalid != 2 || sum.Received != 8 || sum.SendErrors != 0 {
			t.Errorf("summary: T1' %v, missing %d, invalid %d, %+v", sum.TxTimestamps, sum.TxMissing, sum.TxInvalid, sum.Summary)
		}
		kernel := map[uint32]bool{0: true, 2: true, 6: true, 7: true}
		for _, p := range rec.packets {
			seq := p.Record.SenderSeq
			user := t0.Add(time.Duration(seq) * defaultInterval)
			want, rtt := user, 10*time.Millisecond
			if kernel[seq] {
				want, rtt = user.Add(3*time.Microsecond), rtt-3*time.Microsecond
			}
			if p.T1FromKernel != kernel[seq] || !p.Record.T1.Equal(want) || p.Result.Metrics.RTT != rtt {
				t.Errorf("seq %d: kernel %v, T1 %v, RTT %v", seq, p.T1FromKernel, p.Record.T1.Sub(user), p.Result.Metrics.RTT)
			}
		}
	})
}

func TestKernelT1NoWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newTxLink()
		l.stampOf = func(uint32) txBehavior { return txBehavior{after: 11500 * time.Microsecond} }
		cfg := testConfig(3)
		cfg.TxTimestampWait = -1
		sum, rec, err := startRun(t.Context(), t, cfg, l.link, WithConn(l))
		if err != nil {
			t.Fatal(err)
		}
		if sum.TxMissing != 3 {
			t.Errorf("missing %d", sum.TxMissing)
		}
		for _, p := range rec.packets {
			if p.T1FromKernel {
				t.Errorf("seq %d used T1'", p.Record.SenderSeq)
			}
		}
	})
}

func TestTxTracker(t *testing.T) {
	l := newTxLink()
	if newTxTracker(l.link, 0, 1) != nil {
		t.Error("a tracker for a conn without transmit timestamps")
	}
	tr := newTxTracker(l, 0, 3000)
	if len(tr.bySeq) != 4096 || len(tr.byID) != 4096 {
		t.Errorf("ring size %d", len(tr.bySeq))
	}
	tr = newTxTracker(l, 0, 10)
	pkt := make([]byte, wire.SenderHeaderSize)
	send := func(seq uint32) {
		_ = (&wire.SenderPacket{Seq: seq, Timestamp: ntptime.FromTime(time.Now())}).PutHeader(pkt)
		if err := tr.send(pkt, reflectorAddr, seq); err != nil {
			t.Fatal(err)
		}
	}
	send(0)
	send(1024) // the same slot: seq 0 is forgotten
	tr.drain()
	if _, stamped, pending := tr.lookup(0); stamped || pending {
		t.Error("overwritten seq 0 still known")
	}
	if _, stamped, _ := tr.lookup(1024); !stamped {
		t.Error("seq 1024 has no T1'")
	}
	tr.stamp(77, time.Now()) // an identifier never handed out
	if _, outcome := tr.t1(5, time.Now(), time.Now()); outcome != txMissing {
		t.Errorf("unknown seq: %v", outcome)
	}

	l.writeErr = func(uint32, int) error { return syscall.EPERM }
	if err := tr.send(pkt, reflectorAddr, 2); err == nil {
		t.Error("send succeeded")
	}
	if _, stamped, pending := tr.lookup(2); stamped || pending {
		t.Error("an unsent packet awaits T1'")
	}
}

// benchConn is a socket whose kernel stamps every packet at once and which
// keeps the last packet sent, for the hot-path benchmark.
type benchConn struct {
	last   []byte
	n      int
	nextID uint32
	stamps []queuedStamp
}

func (c *benchConn) WriteTo(b []byte, _ netip.AddrPort, _ int) (time.Time, error) {
	c.n = copy(c.last, b)
	return time.Now(), nil
}

func (c *benchConn) WriteToID(b []byte, to netip.AddrPort, tos int) (time.Time, uint32, error) {
	at, _ := c.WriteTo(b, to, tos)
	id := c.nextID
	c.nextID++
	c.stamps = append(c.stamps, queuedStamp{id: id, at: at})
	return at, id, nil
}

func (c *benchConn) ReadTxTimestamps(fn func(uint32, time.Time)) (int, error) {
	n := len(c.stamps)
	for _, s := range c.stamps {
		fn(s.id, s.at)
	}
	c.stamps = c.stamps[:0]
	return n, nil
}

func (c *benchConn) TxTimestamps() bool { return true }

func (c *benchConn) ReadFrom([]byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	return 0, netip.AddrPort{}, tstamp.RxMeta{}, syscall.EAGAIN
}
func (c *benchConn) LocalAddr() netip.AddrPort       { return senderAddr }
func (c *benchConn) SetReadDeadline(time.Time) error { return nil }
func (c *benchConn) Mode() tstamp.Mode               { return tstamp.ModeKernel }
func (c *benchConn) Close() error                    { return nil }

// packetPath returns one turn of the per-packet hot path: send a packet,
// read its T1', reflect it, handle the reply, and retire it.
func packetPath(tb testing.TB) func() {
	c := &benchConn{last: make([]byte, 128), stamps: make([]queuedStamp, 0, 4)}
	cfg := testConfig(0)
	cfg.MaxSamples = 1024
	r := newRun(cfg, c, make([]byte, wire.SenderHeaderSize+DefaultPadding), nil, Hooks{})
	if r.tx == nil {
		tb.Fatal("no T1' tracker")
	}
	w := r.newWindow(0, 0, time.Now())
	r.windows = []*window{w}
	var seq uint32
	return func() {
		r.send(w, seq)
		seq++
		n := copy(r.rbuf, c.last[:c.n])
		var rp wire.ReflectorPacket
		now := time.Now()
		rp.ReceiveTimestamp, rp.Timestamp = ntptime.FromTime(now), ntptime.FromTime(now)
		m, _ := rp.Reflect(r.rbuf, n)
		r.handle(m, reflectorAddr, tstamp.RxMeta{At: now, TTL: 64, TOS: 0})
		r.expire(now.Add(cfg.Timeout + time.Second))
	}
}

func TestPacketPathDoesNotAllocate(t *testing.T) {
	turn := packetPath(t)
	for range 5000 { // past the growth of maps and buffers
		turn()
	}
	if allocs := testing.AllocsPerRun(1000, turn); allocs != 0 {
		t.Errorf("the packet path allocates %v times per packet", allocs)
	}
}

func BenchmarkPacketPath(b *testing.B) {
	turn := packetPath(b)
	b.ReportAllocs()
	for b.Loop() {
		turn()
	}
}
