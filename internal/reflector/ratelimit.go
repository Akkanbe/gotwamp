//declscope:namespace light

package reflector

import (
	"net/netip"
	"sync"
	"time"
)

// maxBuckets caps the per-source buckets, so that spoofed sources cannot grow
// the table without bound.
const maxBuckets = 65536

// fullSweepInterval spaces out the sweeps a full table triggers. A bucket
// refills within one second (the burst is one second of tokens), so sweeping
// more often frees almost nothing and would cost a scan of the whole table
// per packet of every new source.
const fullSweepInterval = time.Second

// bucket is a token bucket of one source address.
type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter applies a token bucket of rate packets per second, with a burst
// of one second, to each source address. A source idle long enough to refill
// its bucket is forgotten, since a fresh bucket behaves the same.
type rateLimiter struct {
	mu        sync.Mutex
	rate      float64
	buckets   map[netip.Addr]*bucket
	lastSweep time.Time // of a sweep a full table triggered
}

func newRateLimiter(pps int) *rateLimiter {
	return &rateLimiter{rate: float64(pps), buckets: make(map[netip.Addr]*bucket)}
}

// allow takes one token for src at now and reports whether there was one.
// When the table is full and a sweep (at most one per fullSweepInterval)
// frees nothing, new sources are refused: the limit was configured for
// protection, so it fails closed.
func (r *rateLimiter) allow(src netip.Addr, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[src]
	if !ok {
		if len(r.buckets) >= maxBuckets {
			if now.Sub(r.lastSweep) < fullSweepInterval && !now.Before(r.lastSweep) {
				return false
			}
			r.lastSweep = now
			r.sweepLocked(now)
			if len(r.buckets) >= maxBuckets {
				return false
			}
		}
		b = &bucket{tokens: r.rate, last: now}
		r.buckets[src] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = min(r.rate, b.tokens+elapsed*r.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep forgets the sources whose bucket is full again at now.
func (r *rateLimiter) sweep(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked(now)
}

func (r *rateLimiter) sweepLocked(now time.Time) {
	for src, b := range r.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*r.rate >= r.rate {
			delete(r.buckets, src)
		}
	}
}
