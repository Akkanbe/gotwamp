package sender

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

var (
	reflectorAddr = netip.MustParseAddrPort("192.0.2.10:862")
	senderAddr    = netip.MustParseAddrPort("198.51.100.5:50000")
)

// behavior is what the network and the fake reflector do with one packet.
type behavior struct {
	drop       bool          // lost on the forward path
	fwd, rev   time.Duration // one-way delays
	forwardDup bool          // the reflector receives it twice, 1 ms apart
	reverseDup bool          // every reply arrives twice, 1 ms apart
}

var normal = behavior{fwd: 5 * time.Millisecond, rev: 5 * time.Millisecond}

type datagram struct {
	b    []byte
	from netip.AddrPort
}

type sentPacket struct {
	at  time.Time
	pkt wire.SenderPacket
	len int
}

// link is an in-memory tstamp.Conn whose peer is a stateful TWAMP Light
// reflector 1 ms of processing away. It runs inside a synctest bubble.
type link struct {
	behave       func(seq uint32) behavior
	writeErr     func(seq uint32, attempt int) error
	readErr      error
	reflectorEst errest.Estimate
	// dscpecn, when set, makes the reflector write the S-DSCP-ECN octet the
	// packet seq arrived with (RFC 7750).
	dscpecn func(seq uint32) wire.DSCPECN
	// replySSID, when set, is written into the replies as a STAMP SSID.
	replySSID uint16

	in   chan datagram
	wake chan struct{}

	mu       sync.Mutex
	deadline time.Time
	rseq     uint32
	attempts map[uint32]int
	sent     []sentPacket
}

func newLink() *link {
	return &link{
		behave:       func(uint32) behavior { return normal },
		reflectorEst: errest.Compute(10*time.Microsecond, true),
		in:           make(chan datagram, 4096),
		wake:         make(chan struct{}, 1),
		attempts:     make(map[uint32]int),
	}
}

func (l *link) WriteTo(b []byte, to netip.AddrPort, tos int) (time.Time, error) {
	now := time.Now()
	var p wire.SenderPacket
	if err := p.Decode(b); err != nil || to != reflectorAddr || tos != -1 {
		panic("bad send")
	}
	l.mu.Lock()
	attempt := l.attempts[p.Seq]
	l.attempts[p.Seq]++
	l.mu.Unlock()
	if l.writeErr != nil {
		if err := l.writeErr(p.Seq, attempt); err != nil {
			return now, err
		}
	}
	p.Padding = bytes.Clone(p.Padding)
	l.mu.Lock()
	l.sent = append(l.sent, sentPacket{at: now, pkt: p, len: len(b)})
	l.mu.Unlock()

	bh := l.behave(p.Seq)
	if bh.drop {
		return now, nil
	}
	l.reflect(b, now.Add(bh.fwd), bh)
	if bh.forwardDup {
		l.reflect(b, now.Add(bh.fwd+time.Millisecond), bh)
	}
	return now, nil
}

func (l *link) reflect(b []byte, t2 time.Time, bh behavior) {
	t3 := t2.Add(time.Millisecond)
	buf := make([]byte, max(len(b), wire.ReflectorDSCPECNHeaderSize))
	copy(buf, b)
	l.mu.Lock()
	rseq := l.rseq
	l.rseq++
	l.mu.Unlock()
	rp := wire.ReflectorPacket{
		Seq:              rseq,
		Timestamp:        ntptime.FromTime(t3),
		ErrorEstimate:    l.reflectorEst,
		ReceiveTimestamp: ntptime.FromTime(t2),
		SenderTTL:        250,
	}
	if l.dscpecn != nil {
		var sp wire.SenderPacket
		_ = sp.Decode(b)
		rp.HasDSCPECN, rp.DSCPECN = true, l.dscpecn(sp.Seq)
	}
	n, err := rp.Reflect(buf, len(b))
	if err != nil {
		panic(err)
	}
	if l.replySSID != 0 {
		binary.BigEndian.PutUint16(buf[14:], l.replySSID)
	}
	l.deliver(buf[:n], reflectorAddr, t3.Add(bh.rev))
	if bh.reverseDup {
		l.deliver(buf[:n], reflectorAddr, t3.Add(bh.rev+time.Millisecond))
	}
}

// deliver makes b arrive from from at at.
func (l *link) deliver(b []byte, from netip.AddrPort, at time.Time) {
	time.AfterFunc(time.Until(at), func() { l.in <- datagram{b: b, from: from} })
}

func (l *link) ReadFrom(b []byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	if l.readErr != nil {
		return 0, netip.AddrPort{}, tstamp.RxMeta{}, l.readErr
	}
	for {
		l.mu.Lock()
		deadline := l.deadline
		l.mu.Unlock()
		var expired <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, netip.AddrPort{}, tstamp.RxMeta{}, os.ErrDeadlineExceeded
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			expired = timer.C
		}
		select {
		case dg := <-l.in:
			n := copy(b, dg.b)
			return n, dg.from, tstamp.RxMeta{At: time.Now(), TTL: 252, TOS: 0}, nil
		case <-expired:
		case <-l.wake:
		}
	}
}

func (l *link) SetReadDeadline(t time.Time) error {
	l.mu.Lock()
	l.deadline = t
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return nil
}

func (l *link) LocalAddr() netip.AddrPort { return senderAddr }
func (l *link) Mode() tstamp.Mode         { return tstamp.ModeUserspace }
func (l *link) Close() error              { return nil }

func (l *link) sentPackets() []sentPacket {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sentPacket(nil), l.sent...)
}

// recorder collects the hook calls.
type recorder struct {
	packets   []PacketResult
	lost      []LostPacket
	summaries []Summary
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		Packet:  func(p PacketResult) { r.packets = append(r.packets, p) },
		Lost:    func(l LostPacket) { r.lost = append(r.lost, l) },
		Summary: func(s Summary) { r.summaries = append(r.summaries, s) },
	}
}

func (r *recorder) kinds() map[stats.Kind]int {
	m := make(map[stats.Kind]int)
	for _, p := range r.packets {
		m[p.Result.Kind]++
	}
	return m
}

func testConfig(count uint32) Config {
	cfg := DefaultConfig(reflectorAddr)
	cfg.Count = count
	cfg.SpinBefore = -1 // a spinning goroutine never lets synctest time advance
	return cfg
}

func startRun(ctx context.Context, t *testing.T, cfg Config, l *link, opts ...Option) (Summary, *recorder, error) {
	t.Helper()
	rec := &recorder{}
	s, err := New(cfg, append([]Option{WithConn(l), WithHooks(rec.hooks())}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sum, err := s.Run(ctx)
	if cerr := s.Close(); cerr != nil {
		t.Errorf("Close: %v", cerr)
	}
	return sum, rec, err
}

func TestCountRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		t0 := time.Now()
		sum, rec, err := startRun(t.Context(), t, testConfig(10), l)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Absolute schedule: packet i leaves at t0 + i × 100 ms, carrying
		// that time as T1.
		sent := l.sentPackets()
		if len(sent) != 10 {
			t.Fatalf("sent %d packets", len(sent))
		}
		for i, p := range sent {
			want := t0.Add(time.Duration(i) * defaultInterval)
			if !p.at.Equal(want) || p.pkt.Seq != uint32(i) || !p.pkt.Timestamp.Time(want).Equal(want) {
				t.Errorf("packet %d: seq %d at %v, T1 %v; want %v", i, p.pkt.Seq, p.at.Sub(t0), p.pkt.Timestamp.Time(want).Sub(t0), want.Sub(t0))
			}
			if p.len != wire.SenderHeaderSize+DefaultPadding || !bytes.Equal(p.pkt.Padding, make([]byte, DefaultPadding)) {
				t.Errorf("packet %d: length %d, padding %x", i, p.len, p.pkt.Padding)
			}
			if p.pkt.ErrorEstimate != errest.Unknown {
				t.Errorf("packet %d: error estimate %v without a clock", i, p.pkt.ErrorEstimate)
			}
		}

		if sum.Sent != 10 || sum.Received != 10 || sum.Lost != 0 || sum.Pending != 0 || sum.Aborted {
			t.Errorf("summary = %+v", sum)
		}
		if sum.RTT.Min != 10*time.Millisecond || sum.RTT.Max != 10*time.Millisecond {
			t.Errorf("RTT = %+v", sum.RTT)
		}
		if !sum.LossSplitValid || sum.ReverseHops.Mode != 3 || sum.ForwardHops.Mode != 5 {
			t.Errorf("split %v, hops %+v %+v", sum.LossSplitValid, sum.ForwardHops, sum.ReverseHops)
		}
		// The run ends with the last reply: sent at 900 ms, back 11 ms later.
		if !sum.StartedAt.Equal(t0) || sum.EndedAt.Sub(t0) != 911*time.Millisecond {
			t.Errorf("run %v → %v", sum.StartedAt.Sub(t0), sum.EndedAt.Sub(t0))
		}
		if sum.Local != senderAddr || sum.Remote != reflectorAddr || sum.Timestamping != tstamp.ModeUserspace {
			t.Errorf("endpoints %v %v %v", sum.Local, sum.Remote, sum.Timestamping)
		}
		if len(rec.packets) != 10 || len(rec.summaries) != 1 || len(rec.lost) != 0 || rec.summaries[0] != sum {
			t.Errorf("hooks: %d packets, %d summaries, %d lost", len(rec.packets), len(rec.summaries), len(rec.lost))
		}
		p := rec.packets[3]
		if p.Record.SenderSeq != 3 || p.Record.ReflectorSeq != 3 || p.Result.Metrics.RTTRaw != 11*time.Millisecond ||
			p.Record.ReplyTTL != 252 || p.Record.SenderTTL != 250 || p.TOS != 0 || p.ReflectorErrorEstimate != l.reflectorEst {
			t.Errorf("packet 3 = %+v", p)
		}
	})
}

func TestTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(seq uint32) behavior {
			switch seq {
			case 3, 7:
				return behavior{drop: true}
			case 5:
				return behavior{fwd: 5 * time.Millisecond, rev: 600 * time.Millisecond}
			}
			return normal
		}
		cfg := testConfig(10)
		cfg.Timeout = 500 * time.Millisecond
		t0 := time.Now()
		// A new session: the reflector numbers from 0, which the direction
		// of loss needs once packets are lost.
		sum, rec, err := startRun(t.Context(), t, cfg, l, withFull())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		// Seq 5 is declared lost at 1 s, so its reply at 1.106 s is stray.
		if sum.Received != 7 || sum.Lost != 3 || sum.Stray != 1 || sum.LossRatio != 0.3 {
			t.Errorf("summary = %+v", sum)
		}
		// Seq 5 reached the reflector, so only two are forward losses.
		if !sum.LossSplitValid || sum.LostForward != 2 || sum.LostReverse != 1 {
			t.Errorf("split %v %d %d", sum.LossSplitValid, sum.LostForward, sum.LostReverse)
		}
		want := []LostPacket{
			{Seq: 3, T1: t0.Add(300 * time.Millisecond)},
			{Seq: 5, T1: t0.Add(500 * time.Millisecond)},
			{Seq: 7, T1: t0.Add(700 * time.Millisecond)},
		}
		if len(rec.lost) != 3 || rec.lost[0] != want[0] || rec.lost[1] != want[1] || rec.lost[2] != want[2] {
			t.Errorf("lost hooks = %v", rec.lost)
		}
		if rec.kinds()[stats.KindStray] != 1 {
			t.Errorf("kinds = %v", rec.kinds())
		}
		// Seq 7 times out at 1.2 s; the receiver notices within 100 ms.
		if d := sum.EndedAt.Sub(t0); d < 1200*time.Millisecond || d > 1300*time.Millisecond {
			t.Errorf("run ended at %v", d)
		}
	})
}

func TestDuplicatesAndReordering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(seq uint32) behavior {
			switch seq {
			case 1:
				return behavior{fwd: 5 * time.Millisecond, rev: 5 * time.Millisecond, forwardDup: true}
			case 2:
				return behavior{fwd: 5 * time.Millisecond, rev: 5 * time.Millisecond, reverseDup: true}
			case 6:
				// Overtaken by 7 and 8 on the way back.
				return behavior{fwd: 5 * time.Millisecond, rev: 250 * time.Millisecond}
			}
			return normal
		}
		sum, rec, err := startRun(t.Context(), t, testConfig(10), l)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if sum.Received != 10 || sum.DupForward != 1 || sum.DupReverse != 1 {
			t.Errorf("summary = %+v", sum)
		}
		if sum.Reordered != 1 || sum.ReorderedReverse != 1 {
			t.Errorf("reordered %d / %d", sum.Reordered, sum.ReorderedReverse)
		}
		k := rec.kinds()
		if k[stats.KindDupForward] != 1 || k[stats.KindDupReverse] != 1 || k[stats.KindAccepted] != 10 {
			t.Errorf("kinds = %v", k)
		}
		var flagged []uint32
		for _, p := range rec.packets {
			if p.Result.Reordered {
				flagged = append(flagged, p.Record.SenderSeq)
			}
		}
		if len(flagged) != 1 || flagged[0] != 6 {
			t.Errorf("reordered packets %v", flagged)
		}
	})
}

func TestDiscards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		t0 := time.Now()
		// A reply for a packet never sent, a datagram from elsewhere, one
		// from the right port of another address, and a short one.
		stray := make([]byte, wire.ReflectorHeaderSize)
		stray[27] = 99 // Sender Sequence Number 99
		l.deliver(stray, reflectorAddr, t0.Add(150*time.Millisecond))
		l.deliver(stray, netip.MustParseAddrPort("192.0.2.11:862"), t0.Add(160*time.Millisecond))
		l.deliver(stray, netip.MustParseAddrPort("192.0.2.10:863"), t0.Add(170*time.Millisecond))
		l.deliver(stray[:40], reflectorAddr, t0.Add(180*time.Millisecond))
		// The reflector's own address as an IPv4-mapped IPv6 address is the
		// same peer.
		l.deliver(stray, netip.MustParseAddrPort("[::ffff:192.0.2.10]:862"), t0.Add(190*time.Millisecond))

		sum, rec, err := startRun(t.Context(), t, testConfig(5), l)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if sum.Received != 5 || sum.Stray != 2 || sum.ForeignSource != 2 || sum.Malformed != 1 {
			t.Errorf("summary = %+v", sum)
		}
		if rec.kinds()[stats.KindStray] != 2 {
			t.Errorf("kinds = %v", rec.kinds())
		}
	})
}

func TestAborted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(uint32) behavior { return behavior{fwd: 100 * time.Millisecond, rev: 100 * time.Millisecond} }
		ctx, cancel := context.WithCancel(t.Context())
		t0 := time.Now()
		time.AfterFunc(450*time.Millisecond, cancel)
		sum, rec, err := startRun(ctx, t, testConfig(100), l)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
		// Seq 0–4 were sent; the replies of 0–2 (back after 201 ms) arrived.
		if !sum.Aborted || sum.Sent != 5 || sum.Received != 3 || sum.Pending != 2 || sum.Lost != 0 || sum.LossRatio != 0 {
			t.Errorf("summary = %+v", sum)
		}
		if sum.EndedAt.Sub(t0) != 450*time.Millisecond {
			t.Errorf("ended at %v", sum.EndedAt.Sub(t0))
		}
		if len(rec.summaries) != 1 || len(l.sentPackets()) != 5 {
			t.Errorf("%d summaries, %d packets sent", len(rec.summaries), len(l.sentPackets()))
		}
	})
}

func TestContinuous(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(seq uint32) behavior {
			if seq == 15 {
				return behavior{drop: true}
			}
			return normal
		}
		cfg := testConfig(0)
		cfg.Window = time.Second
		cfg.Timeout = 500 * time.Millisecond
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(3050*time.Millisecond, cancel)
		t0 := time.Now()
		sum, rec, err := startRun(ctx, t, cfg, l)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
		if len(rec.summaries) != 4 {
			t.Fatalf("%d summaries", len(rec.summaries))
		}
		for i, s := range rec.summaries[:3] {
			lost := uint64(0)
			if i == 1 {
				lost = 1
			}
			if s.Window != i || s.Aborted || s.Sent != 10 || s.Lost != lost || s.Received != 10-lost {
				t.Errorf("window %d = %+v", i, s)
			}
			if !s.StartedAt.Equal(t0.Add(time.Duration(i) * time.Second)) {
				t.Errorf("window %d started at %v", i, s.StartedAt.Sub(t0))
			}
		}
		// The window of seq 30 was cut short by the cancellation.
		last := rec.summaries[3]
		if last != sum || last.Window != 3 || !last.Aborted || last.Sent != 1 || last.Received != 1 {
			t.Errorf("last window = %+v", last)
		}
		for _, p := range rec.packets {
			if want := int(p.Record.SenderSeq / 10); p.Window != want {
				t.Errorf("seq %d in window %d", p.Record.SenderSeq, p.Window)
			}
		}
		if len(rec.lost) != 1 || rec.lost[0].Window != 1 || rec.lost[0].Seq != 15 {
			t.Errorf("lost = %v", rec.lost)
		}
	})
}

func TestErrorEstimate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.reflectorEst = errest.New(true, false, 0, 0) // Multiplier 0 (CSCwa79808)
		clock := WithClock(func() (bool, time.Duration) { return true, time.Millisecond })
		sum, _, err := startRun(t.Context(), t, testConfig(3), l, clock)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		want := errest.Compute(time.Millisecond+defaultUserspaceError, true)
		for _, p := range l.sentPackets() {
			if p.pkt.ErrorEstimate != want {
				t.Errorf("error estimate %v, want %v", p.pkt.ErrorEstimate, want)
			}
		}
		// Tolerated by default, with invalid one-way delays.
		if sum.Received != 3 || sum.OWDValid || !sum.SenderSynced {
			t.Errorf("summary = %+v", sum)
		}

		l = newLink()
		l.reflectorEst = errest.New(true, false, 0, 0)
		cfg := testConfig(3)
		cfg.DropZeroMultiplier = true
		cfg.MethodError = time.Millisecond
		cfg.Timeout = 300 * time.Millisecond
		sum, _, err = startRun(t.Context(), t, cfg, l, clock)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if sum.Received != 0 || sum.Malformed != 3 || sum.Lost != 3 {
			t.Errorf("summary = %+v", sum)
		}
		if got, want := l.sentPackets()[0].pkt.ErrorEstimate, errest.Compute(2*time.Millisecond, true); got != want {
			t.Errorf("error estimate %v, want %v", got, want)
		}
	})
}

func TestPadding(t *testing.T) {
	ones := bytes.Repeat([]byte{0xa5}, 100)
	tests := []struct {
		name string
		fill PaddingFill
		opts []Option
		want byte
	}{
		{"light auto is zero", PaddingAuto, nil, 0},
		{"full auto is random", PaddingAuto, []Option{withFull()}, 0xa5},
		{"explicit random", PaddingRandom, nil, 0xa5},
		{"explicit zero", PaddingZero, []Option{withFull()}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newLink()
				cfg := testConfig(2)
				cfg.Padding = 30
				cfg.PaddingFill = tt.fill
				opts := append([]Option{withRand(bytes.NewReader(ones))}, tt.opts...)
				if _, _, err := startRun(t.Context(), t, cfg, l, opts...); err != nil {
					t.Fatalf("Run: %v", err)
				}
				for _, p := range l.sentPackets() {
					if p.len != 44 || !bytes.Equal(p.pkt.Padding, bytes.Repeat([]byte{tt.want}, 30)) {
						t.Errorf("packet %d: length %d, padding %x", p.pkt.Seq, p.len, p.pkt.Padding)
					}
				}
			})
		})
	}
}

func TestSendErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.writeErr = func(seq uint32, attempt int) error {
			switch {
			case seq == 2 && attempt == 0:
				return &net.OpError{Op: "write", Err: os.NewSyscallError("sendmsg", syscall.ENOBUFS)}
			case seq == 3:
				return syscall.ENOBUFS
			case seq == 5:
				return syscall.EPERM
			}
			return nil
		}
		sum, _, err := startRun(t.Context(), t, testConfig(8), l)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if sum.Sent != 6 || sum.SendErrors != 2 || sum.Received != 6 || sum.Lost != 0 {
			t.Errorf("summary = %+v", sum)
		}
		l.mu.Lock()
		attempts := []int{l.attempts[2], l.attempts[3], l.attempts[5]}
		l.mu.Unlock()
		// ENOBUFS is retried once; other errors are not.
		if attempts[0] != 2 || attempts[1] != 2 || attempts[2] != 1 {
			t.Errorf("attempts = %v", attempts)
		}
	})
}

func TestReadError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.readErr = net.ErrClosed
		sum, rec, err := startRun(t.Context(), t, testConfig(10), l)
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Run error = %v", err)
		}
		if !sum.Aborted || len(rec.summaries) != 1 {
			t.Errorf("summary = %+v", sum)
		}
	})
}

func TestRunOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig(1)
		cfg.LockOSThread = true
		s, err := New(cfg, WithConn(newLink()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(t.Context()); err != nil {
			t.Fatalf("first Run: %v", err)
		}
		if _, err := s.Run(t.Context()); err == nil {
			t.Error("second Run succeeded")
		}
	})
}

func TestSpin(t *testing.T) {
	// Real time: the last stretch before each send is spun.
	l := newLink()
	cfg := testConfig(3)
	cfg.Interval = 2 * time.Millisecond
	cfg.SpinBefore = time.Millisecond
	t0 := time.Now()
	if _, _, err := startRun(t.Context(), t, cfg, l); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, p := range l.sentPackets() {
		if p.at.Before(t0.Add(time.Duration(i) * cfg.Interval)) {
			t.Errorf("packet %d sent %v early", i, t0.Add(time.Duration(i)*cfg.Interval).Sub(p.at))
		}
	}
}

func TestReceivePathAllocations(t *testing.T) {
	l := newLink()
	r := newRun(testConfig(1), l, make([]byte, wire.SenderHeaderSize), nil, Hooks{})
	w := r.newWindow(0, 0, time.Now())
	r.windows = []*window{w}
	t1 := time.Now()
	w.acc.Sent(0, t1)

	b := make([]byte, 41)
	rp := wire.ReflectorPacket{Seq: 0, Timestamp: ntptime.FromTime(t1), ReceiveTimestamp: ntptime.FromTime(t1)}
	pkt := make([]byte, wire.SenderHeaderSize)
	_ = (&wire.SenderPacket{Timestamp: ntptime.FromTime(t1)}).PutHeader(pkt)
	copy(b, pkt)
	n, _ := rp.Reflect(b, len(pkt))
	copy(r.rbuf, b[:n])
	meta := tstamp.RxMeta{At: t1.Add(time.Millisecond), TTL: 60, TOS: -1}

	var got PacketResult
	r.hooks.Packet = func(p PacketResult) { got = p }
	if allocs := testing.AllocsPerRun(100, func() { r.handle(n, reflectorAddr, meta) }); allocs != 0 {
		t.Errorf("handle allocates %v times per reply", allocs)
	}
	if got.Result.Kind != stats.KindDupReverse {
		t.Errorf("last reply = %+v", got)
	}
	if allocs := testing.AllocsPerRun(100, func() { r.expire(meta.At) }); allocs != 0 {
		t.Errorf("expire allocates %v times", allocs)
	}
}

func TestWithConnIsNotClosed(t *testing.T) {
	l := &closeCounter{link: newLink()}
	s, err := New(DefaultConfig(reflectorAddr), WithConn(l))
	if err != nil {
		t.Fatal(err)
	}
	if s.LocalAddr() != senderAddr {
		t.Errorf("LocalAddr = %v", s.LocalAddr())
	}
	if err := s.Close(); err != nil || l.closed != 0 {
		t.Errorf("Close: %v, conn closed %d times", err, l.closed)
	}
}

type closeCounter struct {
	*link
	closed int
}

func (c *closeCounter) Close() error { c.closed++; return nil }

func TestTimestampingErrors(t *testing.T) {
	clock := WithClock(func() (bool, time.Duration) { return true, time.Millisecond })
	cases := []struct {
		name string
		set  func(*Config)
		want time.Duration
	}{
		{"default userspace", func(*Config) {}, time.Millisecond + defaultUserspaceError},
		{"configured userspace", func(c *Config) { c.UserspaceError = 300 * time.Microsecond }, 1300 * time.Microsecond},
		{"kernel error unused by a userspace socket", func(c *Config) { c.KernelError = 900 * time.Microsecond }, time.Millisecond + defaultUserspaceError},
		{"method error overrides", func(c *Config) { c.UserspaceError, c.MethodError = time.Second, 2*time.Millisecond }, 3 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLink()
			cfg := testConfig(1)
			tc.set(&cfg)
			if _, _, err := startRun(t.Context(), t, cfg, l, clock); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got, want := l.sentPackets()[0].pkt.ErrorEstimate, errest.Compute(tc.want, true); got != want {
				t.Errorf("error estimate %v, want %v", got, want)
			}
		})
	}
}

// TestOWDValidity: one-way delays are valid only when both ends are
// synchronized (SPEC §7.4), in the summary and in every packet result.
func TestOWDValidity(t *testing.T) {
	cases := []struct {
		name         string
		senderSynced bool
		reflector    errest.Estimate
		want         bool
	}{
		{"both synchronized", true, errest.Compute(10*time.Microsecond, true), true},
		{"sender unsynchronized", false, errest.Compute(10*time.Microsecond, true), false},
		{"reflector unsynchronized", true, errest.Compute(10*time.Microsecond, false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newLink()
				l.reflectorEst = tc.reflector
				clock := WithClock(func() (bool, time.Duration) { return tc.senderSynced, time.Millisecond })
				sum, rec, err := startRun(t.Context(), t, testConfig(3), l, clock)
				if err != nil {
					t.Fatal(err)
				}
				if sum.OWDValid != tc.want || sum.SenderSynced != tc.senderSynced {
					t.Errorf("summary OWDValid %v, SenderSynced %v", sum.OWDValid, sum.SenderSynced)
				}
				for _, p := range rec.packets {
					if p.Result.Metrics.OWDValid != tc.want || p.Result.Metrics.Forward != 5*time.Millisecond {
						t.Errorf("seq %d: OWDValid %v, forward %v", p.Record.SenderSeq, p.Result.Metrics.OWDValid, p.Result.Metrics.Forward)
					}
				}
			})
		})
	}
}

// TestTransmitPanic: a panic on the send path fails the run, not the
// process (SPEC §12.2).
func TestTransmitPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.writeErr = func(seq uint32, _ int) error {
			if seq == 2 {
				panic("boom")
			}
			return nil
		}
		sum, rec, err := startRun(t.Context(), t, testConfig(10), l)
		if err == nil || !strings.Contains(err.Error(), "transmit panicked: boom") {
			t.Fatalf("Run error = %v", err)
		}
		if !sum.Aborted || len(rec.summaries) != 1 {
			t.Errorf("summary = %+v", sum)
		}
	})
}

type wildcardConn struct{ *link }

func (wildcardConn) LocalAddr() netip.AddrPort { return netip.MustParseAddrPort("0.0.0.0:5000") }

func TestLocalAddr(t *testing.T) {
	c := wildcardConn{newLink()}
	got := localAddr(c, netip.MustParseAddrPort("127.0.0.1:9"))
	if got != netip.MustParseAddrPort("127.0.0.1:5000") {
		t.Errorf("localAddr = %v", got)
	}
	if got := localAddr(newLink(), netip.MustParseAddrPort("127.0.0.1:9")); got != senderAddr {
		t.Errorf("bound address replaced: %v", got)
	}
}

// TestSequenceWrap: where the Sender Sequence Number wraps, a new window
// starts, and nothing is taken for reordering (review m2).
func TestSequenceWrap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		cfg := testConfig(0)
		cfg.Window = time.Second
		cfg.Timeout = 500 * time.Millisecond
		rec := &recorder{}
		r := newRun(cfg, l, make([]byte, 41), nil, rec.hooks())
		r.seqBase = 1<<32 - 3
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(650*time.Millisecond, cancel)
		if _, err := r.execute(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("execute: %v", err)
		}
		if len(rec.summaries) != 2 {
			t.Fatalf("%d summaries", len(rec.summaries))
		}
		first, second := rec.summaries[0], rec.summaries[1]
		if first.Sent != 3 || first.Received != 3 || first.Aborted || first.Reordered != 0 {
			t.Errorf("window before the wrap = %+v", first)
		}
		if second.Sent != 4 || second.Reordered != 0 || second.Window != 1 {
			t.Errorf("window after the wrap = %+v", second)
		}
	})
}

// TestWindowsOfOneSession: the windows of a continuous run are parts of one
// reflected session, and the direction of loss holds across their
// boundaries (Codex review M5).
func TestWindowsOfOneSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.behave = func(seq uint32) behavior {
			switch seq {
			case 10: // the first packet of window 1, lost forward
				return behavior{drop: true}
			case 19: // the last of window 1, back during window 2
				return behavior{fwd: 5 * time.Millisecond, rev: 150 * time.Millisecond}
			case 35: // window 3: reflected, lost on the way back
				return behavior{fwd: 5 * time.Millisecond, rev: 2 * time.Second}
			case 40: // window 4: the first packet, lost forward
				return behavior{drop: true}
			}
			return normal
		}
		cfg := testConfig(0)
		cfg.Window = time.Second
		cfg.Timeout = 500 * time.Millisecond
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(5050*time.Millisecond, cancel)
		_, rec, err := startRun(ctx, t, cfg, l, withFull())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
		want := []struct {
			valid          bool
			lost, fwd, rev uint64
		}{
			{true, 0, 0, 0},  // window 0: a new session counts from 0; every reflection seen
			{true, 1, 1, 0},  // window 1: base from window 0; head loss is forward
			{true, 0, 0, 0},  // window 2: window 1 lost a packet, so its end is unsure; no loss here
			{true, 1, 0, 1},  // window 3: base from window 2; a reflection lost back
			{false, 1, 0, 0}, // window 4: window 3 lost a packet, and this one does too
			{true, 0, 0, 0},  // window 5: no loss
		}
		if len(rec.summaries) < len(want) {
			t.Fatalf("%d summaries", len(rec.summaries))
		}
		for i, w := range want {
			s := rec.summaries[i]
			if s.LossSplitValid != w.valid || s.Lost != w.lost || s.LostForward != w.fwd || s.LostReverse != w.rev {
				t.Errorf("window %d: valid %v lost %d forward %d reverse %d; want %+v",
					i, s.LossSplitValid, s.Lost, s.LostForward, s.LostReverse, w)
			}
		}
	})
}

// TestBlockingHook: hooks run on the receive path, so a hook that blocks
// holds the run, cancellation included, until it returns; then the run
// ends at once (Codex review M1: the daemon must queue its stream sends).
func TestBlockingHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		release := make(chan struct{})
		var blocked sync.Once
		entered := make(chan struct{})
		hooks := Hooks{Packet: func(PacketResult) {
			blocked.Do(func() {
				close(entered)
				<-release
			})
		}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			s, err := New(testConfig(100), WithConn(l), WithHooks(hooks))
			if err != nil {
				done <- err
				return
			}
			_, err = s.Run(ctx)
			done <- err
		}()
		<-entered
		cancel()
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("Run returned while a hook blocked: %v", err)
		default:
		}
		start := time.Now()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("Run took %v to end after the hook returned", d)
		}
	})
}

// TestDSCPECNMonitoring: the DSCP and ECN the reflector saw (RFC 7750).
func TestDSCPECNMonitoring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		l.dscpecn = func(seq uint32) wire.DSCPECN {
			if seq == 1 {
				return wire.NewDSCPECN(10, 3) // remarked and congestion marked
			}
			return wire.NewDSCPECN(46, 0)
		}
		cfg := testConfig(3)
		cfg.DSCP = 46
		cfg.DSCPECNMonitoring = true
		sum, rec, err := startRun(t.Context(), t, cfg, l)
		if err != nil {
			t.Fatal(err)
		}
		want := stats.DSCPReflected{Count: 3, Mode: 46, Remarked: 1, CE: 1}
		if sum.DSCPReflected != want || sum.Malformed != 0 {
			t.Errorf("summary DSCPReflected %+v, malformed %d", sum.DSCPReflected, sum.Malformed)
		}
		if p := rec.packets[1]; !p.Record.HasDSCPECN || p.Record.DSCPECN != 10<<2|3 {
			t.Errorf("packet 1 = %+v", p.Record)
		}

		// A reflector without RFC 7750 sends 41-octet replies: malformed.
		l = newLink()
		cfg.Timeout = 300 * time.Millisecond
		sum, _, err = startRun(t.Context(), t, cfg, l)
		if err != nil {
			t.Fatal(err)
		}
		if sum.Malformed != 3 || sum.Received != 0 || sum.DSCPReflected.Count != 0 {
			t.Errorf("summary = %+v", sum)
		}
	})
}

// authLink is an in-memory tstamp.Conn whose peer is an authenticated or
// encrypted Session-Reflector. Replies to the sequence numbers in forge are
// sealed with other keys.
type authLink struct {
	crypto, forger *wire.TestCrypto
	forge          map[uint32]bool
	sizes          []int
	in             chan datagram
	mu             sync.Mutex
	deadline       time.Time
	rseq           uint32
}

func (l *authLink) WriteTo(b []byte, _ netip.AddrPort, _ int) (time.Time, error) {
	now := time.Now()
	buf := make([]byte, max(len(b), wire.AuthReflectorHeaderSize))
	copy(buf, b)
	if err := l.crypto.OpenSender(buf[:len(b)]); err != nil {
		panic(err)
	}
	var sp wire.AuthSenderPacket
	_ = sp.Decode(buf[:len(b)])
	l.mu.Lock()
	l.sizes = append(l.sizes, len(b))
	rp := wire.AuthReflectorPacket{
		Seq: l.rseq, Timestamp: ntptime.FromTime(now.Add(2 * time.Millisecond)), ErrorEstimate: errest.Compute(time.Microsecond, true),
		ReceiveTimestamp: ntptime.FromTime(now.Add(time.Millisecond)), SenderTTL: 255,
	}
	l.rseq++
	l.mu.Unlock()
	n, _ := rp.Reflect(buf, len(b))
	seal := l.crypto
	if l.forge[sp.Seq] {
		seal = l.forger
	}
	_ = seal.SealReflector(buf[:n])
	time.AfterFunc(3*time.Millisecond, func() { l.in <- datagram{b: buf[:n], from: reflectorAddr} })
	return now, nil
}

func (l *authLink) ReadFrom(b []byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	l.mu.Lock()
	d := l.deadline
	l.mu.Unlock()
	var timeout <-chan time.Time
	if !d.IsZero() {
		timeout = time.After(time.Until(d))
	}
	select {
	case dg := <-l.in:
		return copy(b, dg.b), dg.from, tstamp.RxMeta{At: time.Now(), TTL: 255, TOS: -1}, nil
	case <-timeout:
		return 0, netip.AddrPort{}, tstamp.RxMeta{}, os.ErrDeadlineExceeded
	}
}

func (l *authLink) SetReadDeadline(t time.Time) error {
	l.mu.Lock()
	l.deadline = t
	l.mu.Unlock()
	return nil
}

func (l *authLink) LocalAddr() netip.AddrPort { return senderAddr }
func (l *authLink) Mode() tstamp.Mode         { return tstamp.ModeUserspace }
func (l *authLink) Close() error              { return nil }

// TestAuthenticatedRun runs the Session-Sender in the authenticated and
// encrypted modes: it seals its packets, opens the replies, and counts the
// replies that fail their HMAC as malformed (RFC 4656 §4.2).
func TestAuthenticatedRun(t *testing.T) {
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				keys := wire.DeriveTestKeys(wire.SessionKeys{AES: [16]byte{1}}, wire.SID{2})
				other := keys
				other.HMAC[0] ^= 1
				c, _ := wire.NewTestCrypto(mode, keys)
				f, _ := wire.NewTestCrypto(mode, other)
				l := &authLink{crypto: c, forger: f, forge: map[uint32]bool{2: true}, in: make(chan datagram, 64)}
				cfg := testConfig(5)
				cfg.Timeout = 200 * time.Millisecond
				cfg.Padding = 64
				rec := &recorder{}
				s, err := New(cfg, WithConn(l), WithHooks(rec.hooks()), withFull(), withSecurity(mode, keys))
				if err != nil {
					t.Fatal(err)
				}
				sum, err := s.Run(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if sum.Sent != 5 || sum.Received != 4 || sum.Malformed != 1 || sum.Lost != 1 {
					t.Errorf("summary %+v", sum)
				}
				if l.sizes[0] != wire.AuthSenderHeaderSize+64 {
					t.Errorf("packets of %d octets", l.sizes[0])
				}
				for _, p := range rec.packets {
					if r := p.Record; r.T2.Sub(r.T1) != time.Millisecond || r.T3.Sub(r.T2) != time.Millisecond || r.SenderTTL != 255 {
						t.Errorf("record %+v", r)
					}
				}
			})
		})
	}
	// Mixed mode keeps the unauthenticated 14-octet header.
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		if _, _, err := startRun(t.Context(), t, testConfig(1), l, withSecurity(wire.SecurityMixed, wire.TestKeys{})); err != nil {
			t.Fatal(err)
		}
		if n := l.sentPackets()[0].len; n != wire.SenderHeaderSize+DefaultPadding {
			t.Errorf("mixed: packets of %d octets", n)
		}
	})
}

// modeLink is a link whose socket reports another timestamping mode.
type modeLink struct {
	*link
	mode tstamp.Mode
}

func (l modeLink) Mode() tstamp.Mode { return l.mode }

// TestMethodErrorByMode checks the timestamping error the Error Estimate
// adds for each mode: a hardware socket is at least as good as the kernel's
// (it used to get the user-space error).
func TestMethodErrorByMode(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mode     tstamp.Mode
		hardware time.Duration
		want     time.Duration
	}{
		{"userspace", tstamp.ModeUserspace, time.Microsecond, defaultUserspaceError},
		{"kernel", tstamp.ModeKernel, time.Microsecond, defaultKernelError},
		{"hardware", tstamp.ModeHardware, time.Microsecond, time.Microsecond},
		{"hardware without its own error", tstamp.ModeHardware, 0, defaultKernelError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(1)
			cfg.HardwareError = tt.hardware
			r := newRun(cfg, modeLink{newLink(), tt.mode}, make([]byte, wire.SenderHeaderSize), nil, Hooks{})
			if r.methodError != tt.want {
				t.Errorf("method error %v, want %v", r.methodError, tt.want)
			}
		})
	}
}

// TestSTAMPSender sends STAMP packets (RFC 8762, RFC 8972): the SSID at
// offset 14 and the padding as an Extra Padding TLV after the 44-octet base.
// Replies with zero or the session's SSID are taken; another SSID is not.
func TestSTAMPSender(t *testing.T) {
	for _, tt := range []struct {
		name      string
		replySSID uint16
		received  uint64
	}{
		{"twamp reflector", 0, 3},
		{"stamp reflector", 7, 3},
		{"another session", 8, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := newLink()
				l.replySSID = tt.replySSID
				cfg := testConfig(3)
				cfg.Timeout = 200 * time.Millisecond
				cfg.STAMP, cfg.SSID, cfg.Padding = true, 7, 12
				var sent []byte
				sum, _, err := startRun(t.Context(), t, cfg, l)
				if err != nil {
					t.Fatal(err)
				}
				if sum.Received != tt.received || sum.ForeignSource != 3-tt.received {
					t.Errorf("received %d, foreign %d", sum.Received, sum.ForeignSource)
				}
				p := l.sentPackets()[1]
				if p.len != wire.STAMPBaseSize+12 || p.pkt.Seq != 1 {
					t.Errorf("sent %+v", p)
				}
				sent = append(sent, p.pkt.Padding...) // the octets after offset 14
				if !bytes.Equal(sent[:2], []byte{0, 7}) || !bytes.Equal(sent[30:34], []byte{0x80, 1, 0, 8}) {
					t.Errorf("SSID or TLV header wrong: %x", sent)
				}
			})
		})
	}
}

// TestReflectedMismatch counts replies whose padding lacks the octets to be
// reflected (RFC 6038).
func TestReflectedMismatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := newLink()
		cfg := testConfig(4)
		cfg.Padding = 40
		packet := make([]byte, wire.SenderHeaderSize+40)
		sum, err := runSession(t.Context(), runParams{cfg: cfg, conn: l, packet: packet, reflected: []byte{0, 0}})
		if err != nil || sum.ReflectedMismatch != 0 || sum.Received != 4 {
			t.Errorf("kept padding: %+v, %v", sum, err)
		}
		l = newLink()
		sum, err = runSession(t.Context(), runParams{cfg: cfg, conn: l, packet: packet, reflected: []byte{0xbe, 0xef}})
		if err != nil || sum.ReflectedMismatch != 4 || sum.Received != 4 {
			t.Errorf("other padding: mismatches %d, received %d, %v", sum.ReflectedMismatch, sum.Received, err)
		}
	})
}
