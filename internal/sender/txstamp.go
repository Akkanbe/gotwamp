//declscope:namespace run // the transmitter and receiver's T1' bookkeeping

package sender

import (
	"net/netip"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// Kernel transmit timestamps (T1', SPEC §8.2).
const (
	// defaultTxTimestampWait is tx_timestamp_wait: how long a reply waits
	// for the T1' of its packet.
	defaultTxTimestampWait = time.Millisecond
	// txTimestampSlack is how much earlier than the user-space T1 a T1' may
	// be before it is taken for a mismatch.
	txTimestampSlack = 100 * time.Microsecond
	// txPollInterval is the pause between reads of the error queue while a
	// reply waits for its T1'.
	txPollInterval = 20 * time.Microsecond
)

// txOutcome says which T1 a reply got.
type txOutcome uint8

const (
	// txKernel: the kernel transmit timestamp.
	txKernel txOutcome = iota
	// txMissing: no T1' arrived in time; the user-space T1 is used.
	txMissing
	// txInvalid: T1' contradicted the user-space T1 or T4; T1 is used.
	txInvalid
)

// txSlot is the transmit record of one packet.
type txSlot struct {
	seq     uint32
	id      uint32
	sent    bool // the socket took the packet, as identifier id
	stamped bool // kernel holds T1'
	kernel  time.Time
}

// txTracker pairs packets with their kernel transmit timestamps. Packets are
// found by Sender Sequence Number in bySeq and by OPT_ID identifier in byID,
// two rings of the same power-of-two size, allocated once.
type txTracker struct {
	tt   tstamp.TxTimestamper
	wait time.Duration

	mu    sync.Mutex
	mask  uint32
	bySeq []txSlot
	byID  []uint32 // id&mask → seq; valid when bySeq agrees
	onTx  func(id uint32, at time.Time)
}

// newTxTracker returns a tracker for conn, or nil when conn gives no
// transmit timestamps. inFlight is about how many packets may await a
// reply at once; older records are overwritten.
func newTxTracker(conn tstamp.Conn, wait time.Duration, inFlight int) *txTracker {
	tt, ok := conn.(tstamp.TxTimestamper)
	if !ok || !tt.TxTimestamps() {
		return nil
	}
	size := uint32(1024)
	for int(size) < inFlight && size < 1<<16 {
		size <<= 1
	}
	t := &txTracker{tt: tt, wait: wait, mask: size - 1, bySeq: make([]txSlot, size), byID: make([]uint32, size)}
	t.onTx = t.stamp
	return t
}

// send sends b as packet seq and records its identifier. The record is made
// under the lock that the timestamp reader takes, so no timestamp can come
// back before its packet is known.
func (t *txTracker) send(b []byte, to netip.AddrPort, seq uint32) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := &t.bySeq[seq&t.mask]
	*s = txSlot{seq: seq}
	_, id, err := t.tt.WriteToID(b, to, -1)
	if err != nil {
		return err
	}
	s.id, s.sent = id, true
	t.byID[id&t.mask] = seq
	return nil
}

// stamp records the T1' of packet id; it is the callback of
// ReadTxTimestamps.
func (t *txTracker) stamp(id uint32, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seq := t.byID[id&t.mask]
	if s := &t.bySeq[seq&t.mask]; s.seq == seq && s.sent && s.id == id {
		s.kernel, s.stamped = at, true
	}
}

// drain reads the transmit timestamps queued so far. A failure only means
// fewer T1'; the replies fall back to the user-space T1.
func (t *txTracker) drain() {
	_, _ = t.tt.ReadTxTimestamps(t.onTx)
}

// lookup returns the T1' of seq and whether it may still come.
func (t *txTracker) lookup(seq uint32) (kernel time.Time, stamped, pending bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.bySeq[seq&t.mask]
	if s.seq != seq || !s.sent {
		return time.Time{}, false, false
	}
	return s.kernel, s.stamped, !s.stamped
}

// t1 returns the T1 of a reply to packet seq whose user-space T1 is user and
// whose T4 is t4: T1' when it arrives within the wait and agrees with both
// (SPEC §8.2), user otherwise.
func (t *txTracker) t1(seq uint32, user, t4 time.Time) (time.Time, txOutcome) {
	kernel, stamped, pending := t.lookup(seq)
	if pending {
		deadline := time.Now().Add(t.wait)
		for {
			t.drain()
			if kernel, stamped, pending = t.lookup(seq); !pending || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(txPollInterval)
		}
	}
	switch {
	case !stamped:
		return user, txMissing
	case kernel.Before(user.Add(-txTimestampSlack)) || kernel.After(t4):
		return user, txInvalid
	}
	return kernel, txKernel
}
