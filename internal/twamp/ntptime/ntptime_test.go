package ntptime_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
)

var (
	ntpEpoch    = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	unixEpoch   = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	eraRollover = time.Date(2036, 2, 7, 6, 28, 16, 0, time.UTC)
)

func TestFromTimeKnownValues(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		sec  uint32
		frac uint32
	}{
		{"ntp epoch", ntpEpoch, 0, 0},
		{"unix epoch", unixEpoch, 2208988800, 0},
		{"half second", unixEpoch.Add(500 * time.Millisecond), 2208988800, 1 << 31},
		{"quarter second", unixEpoch.Add(250 * time.Millisecond), 2208988800, 1 << 30},
		{"one nanosecond", unixEpoch.Add(time.Nanosecond), 2208988800, 4}, // 4.294967296 rounds to 4
		{"last nanosecond", unixEpoch.Add(time.Second - time.Nanosecond), 2208988800, 4294967292},
		{"last second of era 0", eraRollover.Add(-time.Second), math.MaxUint32, 0},
		{"era 1 start", eraRollover, 0, 0},
		{"era 1 plus one", eraRollover.Add(time.Second + 500*time.Millisecond), 1, 1 << 31},
		{"before 1900", ntpEpoch.Add(-time.Second), math.MaxUint32, 0},
		{"2026", time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), 4000060800, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := ntptime.FromTime(tt.in)
			if ts.Seconds() != tt.sec || ts.Fraction() != tt.frac {
				t.Errorf("FromTime(%v) = %d.%d, want %d.%d", tt.in, ts.Seconds(), ts.Fraction(), tt.sec, tt.frac)
			}
			if ts != ntptime.New(tt.sec, tt.frac) {
				t.Errorf("FromTime(%v) = %#x, want New(%d, %d)", tt.in, uint64(ts), tt.sec, tt.frac)
			}
		})
	}
}

func TestTimeEraSelection(t *testing.T) {
	tests := []struct {
		name string
		ts   ntptime.Timestamp
		ref  time.Time
		want time.Time
	}{
		{"epoch near epoch", 0, ntpEpoch, ntpEpoch},
		{"epoch near 1968", 0, time.Date(1968, 1, 1, 0, 0, 0, 0, time.UTC), ntpEpoch},
		{"zero near 2026 is era 1 start", 0, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), eraRollover},
		{"era 0 end seen just after rollover", ntptime.New(math.MaxUint32, 1<<31), eraRollover.Add(time.Second), eraRollover.Add(-500 * time.Millisecond)},
		{"era 1 start seen just before rollover", ntptime.New(0, 0), eraRollover.Add(-time.Second), eraRollover},
		{"era 1 value seen from 2100", ntptime.New(10, 0), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), eraRollover.Add(10 * time.Second)},
		{"fraction rounding up to next second", ntptime.New(2208988800, math.MaxUint32), unixEpoch, unixEpoch.Add(time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ts.Time(tt.ref)
			if !got.Equal(tt.want) {
				t.Errorf("Time(ref=%v) = %v, want %v", tt.ref, got, tt.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("Time() location = %v, want UTC", got.Location())
			}
		})
	}
}

func TestTimeWindowEdges(t *testing.T) {
	ref := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	half := time.Duration(1<<31) * time.Second
	lo := ref.Add(-half)
	if got := ntptime.FromTime(lo).Time(ref); !got.Equal(lo) {
		t.Errorf("lower edge: got %v, want %v", got, lo)
	}
	hi := ref.Add(half - time.Second)
	if got := ntptime.FromTime(hi).Time(ref); !got.Equal(hi) {
		t.Errorf("upper edge: got %v, want %v", got, hi)
	}
	// One second past the upper edge wraps to the previous era.
	past := ref.Add(half)
	if got := ntptime.FromTime(past).Time(ref); !got.Equal(past.Add(-time.Duration(1<<32) * time.Second)) {
		t.Errorf("past upper edge: got %v", got)
	}
}

func TestRoundTripExactNanoseconds(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // reproducible test inputs
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 100_000 {
		in := base.Add(time.Duration(r.Int64N(int64(80 * 365 * 24 * time.Hour))))
		got := ntptime.FromTime(in).Time(in.Add(time.Duration(r.Int64N(int64(time.Hour)))))
		if !got.Equal(in) {
			t.Fatalf("round trip of %v gave %v", in, got)
		}
	}
	// Every sub-second value near the boundaries survives.
	for _, ns := range []int{0, 1, 2, 3, 499_999_999, 500_000_000, 500_000_001, 999_999_998, 999_999_999} {
		in := eraRollover.Add(time.Duration(ns))
		if got := ntptime.FromTime(in).Time(in); !got.Equal(in) {
			t.Errorf("round trip of %v gave %v", in, got)
		}
	}
}

func TestMonotonic(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // reproducible test inputs
	prev := ntptime.FromTime(unixEpoch)
	cur := unixEpoch
	for range 100_000 {
		cur = cur.Add(time.Duration(r.Int64N(1000)))
		ts := ntptime.FromTime(cur)
		if ts < prev {
			t.Fatalf("FromTime not monotonic at %v: %#x < %#x", cur, uint64(ts), uint64(prev))
		}
		prev = ts
	}
	// Across the rollover the raw value wraps, but Sub stays positive.
	before := ntptime.FromTime(eraRollover.Add(-time.Nanosecond))
	after := ntptime.FromTime(eraRollover)
	if d := after.Sub(before); d != time.Nanosecond {
		t.Errorf("Sub across rollover = %v, want 1ns", d)
	}
}

func TestSub(t *testing.T) {
	tests := []struct {
		name string
		a, b time.Time
	}{
		{"positive", unixEpoch.Add(1500 * time.Millisecond), unixEpoch},
		{"negative", unixEpoch, unixEpoch.Add(1500*time.Millisecond + 7)},
		{"sub-nanosecond steps", unixEpoch.Add(123), unixEpoch.Add(122)},
		{"across rollover", eraRollover.Add(3 * time.Second), eraRollover.Add(-2*time.Second - 250*time.Millisecond)},
		{"large", unixEpoch.Add(1 << 30 * time.Second), unixEpoch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ntptime.FromTime(tt.a).Sub(ntptime.FromTime(tt.b))
			if want := tt.a.Sub(tt.b); got != want {
				t.Errorf("Sub = %v, want %v", got, want)
			}
		})
	}
}

func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want ntptime.Timestamp
	}{
		{0, 0},
		{-time.Second, 0},
		{2 * time.Second, ntptime.New(2, 0)},
		{1500 * time.Millisecond, ntptime.New(1, 1<<31)},
		{900 * time.Second, ntptime.New(900, 0)},
		{time.Duration(math.MaxUint32)*time.Second + 999_999_999, ntptime.New(math.MaxUint32, 4294967292)},
	}
	for _, tt := range tests {
		got := ntptime.FromDuration(tt.d)
		if got != tt.want {
			t.Errorf("FromDuration(%v) = %d.%d, want %d.%d", tt.d, got.Seconds(), got.Fraction(), tt.want.Seconds(), tt.want.Fraction())
		}
		if tt.d >= 0 && got.Duration() != tt.d {
			t.Errorf("FromDuration(%v).Duration() = %v", tt.d, got.Duration())
		}
	}
	if got := ntptime.FromDuration(math.MaxInt64); got != math.MaxUint64 {
		t.Errorf("FromDuration(2^32 s) = %#x, want saturation", uint64(got))
	}
	if got := ntptime.New(math.MaxUint32, math.MaxUint32).Duration(); got != time.Duration(1<<32)*time.Second {
		t.Errorf("max Duration() = %v", got)
	}
}

func TestIsZero(t *testing.T) {
	if !ntptime.Timestamp(0).IsZero() {
		t.Error("zero timestamp not IsZero")
	}
	if ntptime.New(0, 1).IsZero() {
		t.Error("non-zero fraction reported IsZero")
	}
}

func FuzzTime(f *testing.F) {
	f.Add(uint64(0), int64(0), int64(0))
	f.Add(uint64(math.MaxUint64), eraRollover.Unix(), int64(999_999_999))
	f.Fuzz(func(t *testing.T, raw uint64, refSec, refNsec int64) {
		// Keep the reference inside the range time.Unix can represent.
		ref := time.Unix(refSec%(1<<40), refNsec%1_000_000_000)
		ts := ntptime.Timestamp(raw)
		got := ts.Time(ref)
		back := ntptime.FromTime(got)
		// A fraction within 0.5 ns of the next second rounds up to it.
		roundedUp := ts.Fraction() > math.MaxUint32-3 && back.Seconds() == ts.Seconds()+1
		if back.Seconds() != ts.Seconds() && !roundedUp {
			t.Fatalf("seconds changed: %#x -> %v -> %#x", raw, got, uint64(back))
		}
		if diff := got.Sub(ref.Truncate(time.Second)); diff < -(1<<31)*time.Second || diff > (1<<31)*time.Second {
			t.Fatalf("Time(%v) = %v is outside the era window", ref, got)
		}
	})
}
