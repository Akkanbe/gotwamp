//declscope:namespace light

package reflector

import (
	"net/netip"
	"testing"
	"time"
)

func addr(i int) netip.Addr {
	return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
}

// TestRateLimiterFullTable checks that a table full of busy sources refuses
// new ones without scanning the table for each of them, and that it admits
// them again once a sweep can free refilled buckets.
func TestRateLimiterFullTable(t *testing.T) {
	r := newRateLimiter(1)
	now := time.Unix(1000, 0)
	for i := range maxBuckets {
		if !r.allow(addr(i), now) {
			t.Fatalf("source %d refused while the table had room", i)
		}
	}
	// Every bucket is empty now. The first new source sweeps and is refused.
	if r.allow(netip.MustParseAddr("192.0.2.1"), now) {
		t.Fatal("new source admitted into a full table of busy sources")
	}
	swept := r.lastSweep
	// Within the interval no further sweep runs.
	later := now.Add(fullSweepInterval / 2)
	if r.allow(netip.MustParseAddr("192.0.2.2"), later) || r.lastSweep != swept {
		t.Fatal("a second sweep ran within fullSweepInterval")
	}
	// Known sources still get their refilled tokens meanwhile.
	if !r.allow(addr(7), now.Add(fullSweepInterval)) {
		t.Error("known source refused after refilling")
	}
	// After the interval the buckets are full again and a sweep frees them.
	if !r.allow(netip.MustParseAddr("192.0.2.3"), now.Add(2*fullSweepInterval)) {
		t.Error("new source refused after the buckets refilled")
	}
	if len(r.buckets) > maxBuckets {
		t.Errorf("table grew to %d", len(r.buckets))
	}
}

func BenchmarkRateLimiterFullTable(b *testing.B) {
	r := newRateLimiter(1)
	now := time.Unix(1000, 0)
	for i := range maxBuckets {
		r.allow(addr(i), now)
	}
	b.ResetTimer()
	for i := range b.N {
		r.allow(netip.AddrFrom4([4]byte{172, byte(i >> 16), byte(i >> 8), byte(i)}), now)
	}
}
