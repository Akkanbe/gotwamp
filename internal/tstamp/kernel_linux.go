//go:build linux

package tstamp

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// setsockoptInt is unix.SetsockoptInt. Tests replace it to simulate kernels
// that refuse an option.
var setsockoptInt = unix.SetsockoptInt

const (
	// rxTimestamping asks for software receive timestamps, reported in
	// ts[0] of SCM_TIMESTAMPING (SPEC §8.1).
	rxTimestamping = unix.SOF_TIMESTAMPING_RX_SOFTWARE | unix.SOF_TIMESTAMPING_SOFTWARE
	// txTimestamping adds software transmit timestamps on the error queue,
	// identified by a per-socket packet counter and without the packet
	// itself (SPEC §8.2).
	txTimestamping = unix.SOF_TIMESTAMPING_TX_SOFTWARE | unix.SOF_TIMESTAMPING_OPT_ID | unix.SOF_TIMESTAMPING_OPT_TSONLY
	// errQueueOOBSize holds SCM_TIMESTAMPING and IP(V6)_RECVERR with its
	// offender address.
	errQueueOOBSize = 256
)

// enableKernelTimestamping switches the socket to kernel software receive
// timestamps, adding the card's (hw) and transmit timestamps (tx) when asked.
// It tries the richest set of flags first and drops what the kernel refuses:
// the card's, then transmit timestamps; where SO_TIMESTAMPING is refused
// altogether, SO_TIMESTAMPNS still gives receive timestamps. It reports what
// is on, and fails only when the kernel gives no receive timestamps at all.
//
//declscope:package // called by Listen when the requested mode allows kernel timestamps
func enableKernelTimestamping(rc syscall.RawConn, hw, tx bool) (txOn, hwOn bool, err error) {
	type attempt struct{ hw, tx bool }
	var attempts []attempt
	for _, a := range []attempt{{true, true}, {true, false}, {false, true}} {
		if (!a.hw || hw) && (!a.tx || tx) {
			attempts = append(attempts, a)
		}
	}
	var serr error
	cerr := rc.Control(func(fd uintptr) {
		s := int(fd)
		for _, a := range attempts {
			flags := rxTimestamping
			if a.hw {
				flags |= rxHardware
			}
			if a.tx {
				flags |= txTimestamping
				if a.hw {
					flags |= txHardware
				}
			}
			if setsockoptInt(s, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, flags) == nil {
				txOn, hwOn = a.tx, a.hw
				return
			}
		}
		e1 := setsockoptInt(s, unix.SOL_SOCKET, unix.SO_TIMESTAMPING, rxTimestamping)
		if e1 == nil {
			return
		}
		e2 := setsockoptInt(s, unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)
		if e2 == nil {
			return
		}
		serr = errors.Join(fmt.Errorf("SO_TIMESTAMPING: %w", e1), fmt.Errorf("SO_TIMESTAMPNS: %w", e2))
	})
	if cerr != nil {
		return false, false, cerr
	}
	return txOn, hwOn, serr
}

// txQueue reads transmit timestamps from the error queue. Its buffers and
// the callback handed to RawConn.Control are made once, so that reading
// does not allocate.
//
//declscope:package // a Conn with transmit timestamps holds one
type txQueue struct {
	rc    syscall.RawConn
	oob   []byte
	msg   unix.Msghdr
	drain func(fd uintptr)

	// Set for the duration of one read.
	fn  func(id uint32, at time.Time)
	n   int
	err error
}

//declscope:package // Listen creates one for a Conn with transmit timestamps
func newTxQueue(rc syscall.RawConn) *txQueue {
	q := &txQueue{rc: rc, oob: make([]byte, errQueueOOBSize)}
	q.drain = q.drainFD
	return q
}

// read drains the error queue without waiting.
//
//declscope:package // ReadTxTimestamps delegates to it
func (q *txQueue) read(fn func(id uint32, at time.Time)) (int, error) {
	q.fn, q.n, q.err = fn, 0, nil
	cerr := q.rc.Control(q.drain)
	q.fn = nil
	if cerr != nil {
		return q.n, fmt.Errorf("tstamp: read error queue: %w", cerr)
	}
	if q.err != nil {
		return q.n, fmt.Errorf("tstamp: read error queue: %w", q.err)
	}
	return q.n, nil
}

func (q *txQueue) drainFD(fd uintptr) {
	for {
		q.msg = unix.Msghdr{Control: &q.oob[0]}
		q.msg.SetControllen(len(q.oob))
		_, _, errno := unix.Syscall(unix.SYS_RECVMSG, fd,
			uintptr(unsafe.Pointer(&q.msg)), //nolint:gosec // G103: recvmsg(2) takes the msghdr by pointer
			unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		switch errno {
		case 0:
		case unix.EINTR:
			continue
		case unix.EAGAIN:
			return
		default:
			q.err = errno
			return
		}
		if q.msg.Flags&unix.MSG_CTRUNC != 0 {
			continue
		}
		if id, at, ok := parseTxTimestamp(q.oob[:q.msg.Controllen]); ok {
			q.fn(id, at)
			q.n++
		}
	}
}
