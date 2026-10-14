package reflector

import (
	"errors"
	"log/slog"
	"net/netip"
	"syscall"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// This file is the part of reflecting one packet that the Light reflector
// and the Full Session-Reflector (M3) share: SPEC §6.4 steps 4–6, 8 and 9.

// ttlUnknown is the Sender TTL written when the receive TTL cannot be read
// (RFC 5357 §4.2.1 MUST).
const ttlUnknown = 255

// estimateRefresh is how often a listener recomputes its Error Estimate.
const estimateRefresh = time.Second

// readErrorBackoff spaces out reads after a socket error that is neither a
// timeout nor a close, so that a persistent error cannot spin the CPU.
const readErrorBackoff = 10 * time.Millisecond

// readErrorLogEvery bounds how often a read loop whose socket keeps failing
// logs: the backoff alone would let it warn a hundred times a second.
const readErrorLogEvery = time.Minute

// readErrors handles the read errors of one read loop, which owns it: it
// logs the first at once and later ones at most once per readErrorLogEvery,
// with how many it left out, then backs off.
//
//declscope:package // the Light and Full read loops keep one each
type readErrors struct {
	log     *slog.Logger
	last    time.Time
	skipped int
}

// failed reports err with attrs and waits readErrorBackoff.
//
//declscope:package // the Light and Full read loops call it
func (r *readErrors) failed(msg string, err error, attrs ...any) {
	if now := time.Now(); r.last.IsZero() || now.Sub(r.last) >= readErrorLogEvery {
		r.log.Warn(msg, append(attrs, "err", err, "suppressed", r.skipped)...)
		r.last, r.skipped = now, 0
	} else {
		r.skipped++
	}
	time.Sleep(readErrorBackoff)
}

// ClockState reports whether the clock is synchronized and its maximum error.
// internal/clock provides it; a nil ClockState means unknown.
type ClockState func() (synchronized bool, maxErr time.Duration)

// estimator caches the reflector's Error Estimate for one goroutine.
//
//declscope:package // one per listener goroutine of light.go
type estimator struct {
	clock       ClockState
	methodError time.Duration
	// value is the cached estimate.
	//
	//declscope:private
	value errest.Estimate
	// at is when value was computed.
	//
	//declscope:private
	at time.Time
}

// reflectorMethodError is the timestamping error of a reflector's Error
// Estimate, which covers both of its timestamps (RFC 5357 §4.2.1): T2 may be
// the kernel's, but T3 is always taken in user space right before sendmsg,
// so the larger error applies (SPEC §8.2).
//
//declscope:package // the Light and Full reflectors build their estimators with it
func reflectorMethodError(userspace, kernel time.Duration) time.Duration {
	return max(userspace, kernel)
}

// estimate returns the Error Estimate at now (SPEC §8.4), recomputing it at
// most once per estimateRefresh. Without a clock it is errest.Unknown.
//
//declscope:package // called per packet by light.go
func (e *estimator) estimate(now time.Time) errest.Estimate {
	if e.clock == nil {
		return errest.Unknown
	}
	if e.at.IsZero() || now.Sub(e.at) >= estimateRefresh || now.Before(e.at) {
		synced, maxErr := e.clock()
		e.value = errest.Compute(maxErr+e.methodError, synced)
		e.at = now
	}
	return e.value
}

// replyFormat selects the layout of a reply.
//
//declscope:package // the Light and Full reflectors choose it per listener or session
type replyFormat struct {
	// dscpecn takes the RFC 7750 layout: the S-DSCP-ECN octet at offset 41
	// (or 81 when authenticated) carries the DSCP and ECN of the received
	// packet, which is the TOS / Traffic Class octet as received (0 when the
	// socket could not read it), and the reply is at least 44 octets.
	dscpecn bool
	// symmetric keeps the padding in place (RFC 6038 Symmetrical Size): the
	// reply is as long as the request.
	symmetric bool
	// stamp answers in the STAMP format (RFC 8762 §4.3.1): the SSID copied,
	// a 44-octet base, the octets after it copied back with any TLVs marked
	// as not understood (RFC 8972 §4, wire.MarkSTAMPTLVs).
	stamp bool
}

// buildReply turns the Session-Sender packet in b[:n] into the reply in place
// (SPEC §6.4 steps 4–6) in format f and returns the reply length, max(n, 41)
// (44 with dscpecn or stamp). The caller has checked n against the length
// limits; b must hold at least 44 octets.
//
//declscope:package // shared by the Light and Full reflectors
func buildReply(b []byte, n int, seq uint32, ee errest.Estimate, rx tstamp.RxMeta, f replyFormat) (int, error) {
	p := wire.ReflectorPacket{
		Seq:              seq,
		ErrorEstimate:    ee,
		ReceiveTimestamp: ntptime.FromTime(rx.At),
		SenderTTL:        senderTTL(rx),
		HasDSCPECN:       f.dscpecn,
		SymmetricalSize:  f.symmetric,
	}
	if f.stamp {
		m, err := p.ReflectSTAMP(b, n)
		if err == nil {
			wire.MarkSTAMPTLVs(p.Padding)
		}
		return m, err
	}
	if f.dscpecn && rx.TOS >= 0 && rx.TOS <= 255 {
		p.DSCPECN = wire.DSCPECN(rx.TOS)
	}
	return p.Reflect(b, n)
}

// replySealer protects the replies of an authenticated or encrypted session.
// It keeps the plaintext header, so that a retry can take T3 again and seal
// anew: in encrypted mode T3 lies under the HMAC and the cipher.
//
//declscope:package // the Full reflector builds one per session; sendReply uses it
type replySealer struct {
	crypto *wire.TestCrypto
	plain  [wire.AuthReflectorHeaderSize]byte
}

// stamp writes T3 into the plaintext header, then the HMAC, and encrypts.
func (r *replySealer) stamp(reply []byte, t3 ntptime.Timestamp) error {
	copy(reply, r.plain[:])
	_ = wire.PutAuthTimestamp(reply, t3) // cannot fail: the reply holds the header
	return r.crypto.SealReflector(reply)
}

// senderTTL returns the Sender TTL of a reply: the received TTL or Hop
// Limit, 255 when the socket could not read it (RFC 5357 §4.2.1 MUST).
//
//declscope:package // the unauthenticated and authenticated replies share it
func senderTTL(rx tstamp.RxMeta) uint8 {
	if rx.TTL >= 0 && rx.TTL <= 255 {
		return uint8(rx.TTL)
	}
	return ttlUnknown
}

// sendReply writes T3 into the reply and sends it at once (SPEC §6.4 step 8).
// When the socket buffer is full it takes T3 again and retries once (step 9).
// A non-nil sealer seals the authenticated or encrypted reply after T3.
// A valid from is the source address of the reply: the address a socket
// bound to the wildcard was reached at, so that the Session-Sender sees the
// reply come from where it sent (SPEC §6.1). It needs a tstamp.SourceWriter;
// other sockets let the route choose.
//
//declscope:package // shared by the Light and Full reflectors
func sendReply(conn tstamp.Conn, reply []byte, to netip.AddrPort, from netip.Addr, tos int, sealer *replySealer) error {
	sw, fromOK := conn.(tstamp.SourceWriter)
	fromOK = fromOK && from.IsValid()
	if sealer != nil {
		if len(reply) < wire.AuthReflectorHeaderSize {
			return errShortReply
		}
		copy(sealer.plain[:], reply)
	}
	var err error
	for range 2 {
		t3 := ntptime.FromTime(time.Now())
		if sealer != nil {
			err = sealer.stamp(reply, t3)
		} else {
			err = wire.PutTimestamp(reply, t3)
		}
		if err != nil {
			return err
		}
		if fromOK {
			_, err = sw.WriteToFrom(reply, to, from, tos)
		} else {
			_, err = conn.WriteTo(reply, to, tos)
		}
		if err == nil || !retryable(err) {
			return err
		}
	}
	return err
}

var errShortReply = errors.New("reply shorter than its header")

// retryable reports whether a send failed only because the socket or device
// queue was momentarily full.
func retryable(err error) bool {
	return errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.EAGAIN)
}
