package errest_test

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
)

func TestFields(t *testing.T) {
	tests := []struct {
		raw        uint16
		s, z       bool
		scale, mul uint8
	}{
		{0x0000, false, false, 0, 0},
		{0x8000, true, false, 0, 0},
		{0x4000, false, true, 0, 0},
		{0x3FFF, false, false, 63, 255},
		{0xFFFF, true, true, 63, 255},
		{0x8AD2, true, false, 10, 210},
		{0x0101, false, false, 1, 1},
	}
	for _, tt := range tests {
		e := errest.Estimate(tt.raw)
		if e.Synchronized() != tt.s || e.PTP() != tt.z || e.Scale() != tt.scale || e.Multiplier() != tt.mul {
			t.Errorf("%#04x: got S=%v Z=%v scale=%d mul=%d", tt.raw, e.Synchronized(), e.PTP(), e.Scale(), e.Multiplier())
		}
		if got := errest.New(tt.s, tt.z, tt.scale, tt.mul); got != e {
			t.Errorf("New(%v,%v,%d,%d) = %#04x, want %#04x", tt.s, tt.z, tt.scale, tt.mul, uint16(got), tt.raw)
		}
	}
	if got := errest.New(false, false, 0xFF, 1); got.Scale() != 63 || got.PTP() || got.Synchronized() {
		t.Errorf("New with scale 0xFF leaked into S/Z: %#04x", uint16(got))
	}
	if errest.Unknown != 0x3FFF {
		t.Errorf("Unknown = %#04x, want 0x3fff", uint16(errest.Unknown))
	}
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name  string
		err   time.Duration
		sync  bool
		scale uint8
		mul   uint8
	}{
		{"zero is the smallest error", 0, true, 0, 1},
		{"negative is the smallest error", -time.Second, false, 0, 1},
		{"1ns", time.Nanosecond, true, 0, 5},         // ceil(4.29)
		{"59ns", 59 * time.Nanosecond, true, 0, 254}, // ceil(253.4)
		{"60ns", 60 * time.Nanosecond, true, 1, 129}, // 257.7 -> scale 1, ceil(128.8)
		{"kernel 5us", 5 * time.Microsecond, true, 7, 168},
		{"userspace 50us", 50 * time.Microsecond, true, 10, 210},
		{"1ms", time.Millisecond, false, 15, 132},
		{"1s exactly", time.Second, true, 25, 128},
		{"1s + 1ns", time.Second + time.Nanosecond, true, 25, 129},
		{"16s exactly", 16 * time.Second, true, 29, 128},
		{"255s", 255 * time.Second, true, 32, 255},
		{"256s", 256 * time.Second, true, 33, 128},
		{"max duration", math.MaxInt64, false, 58, 138},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := errest.Compute(tt.err, tt.sync)
			if e.Scale() != tt.scale || e.Multiplier() != tt.mul || e.Synchronized() != tt.sync || e.PTP() {
				t.Fatalf("Compute(%v) = S=%v Z=%v scale=%d mul=%d, want scale=%d mul=%d", tt.err, e.Synchronized(), e.PTP(), e.Scale(), e.Multiplier(), tt.scale, tt.mul)
			}
			if tt.err > 0 && e.Duration() < tt.err {
				t.Errorf("Compute(%v).Duration() = %v understates the error", tt.err, e.Duration())
			}
		})
	}
}

// TestComputeMatchesIterativeRule checks Compute against the literal SPEC §8.4
// procedure: e = ceil(err / 2^-32), then halve with ceiling until e ≤ 255.
func TestComputeMatchesIterativeRule(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6)) //nolint:gosec // reproducible test inputs
	for range 50_000 {
		var d time.Duration
		switch r.IntN(3) {
		case 0:
			d = time.Duration(r.Int64N(int64(time.Microsecond)))
		case 1:
			d = time.Duration(r.Int64N(int64(10 * time.Second)))
		default:
			d = time.Duration(r.Int64N(math.MaxInt64))
		}
		scale, mul := iterative(d)
		e := errest.Compute(d, false)
		if e.Scale() != scale || e.Multiplier() != mul {
			t.Fatalf("Compute(%v) = scale %d mul %d, iterative rule gives scale %d mul %d", d, e.Scale(), e.Multiplier(), scale, mul)
		}
		if e.Duration() < d {
			t.Fatalf("Compute(%v).Duration() = %v understates the error", d, e.Duration())
		}
	}
}

func iterative(d time.Duration) (uint8, uint8) {
	// ceil(d_ns × 2^32 / 1e9) in exact 128-bit arithmetic via float-free steps.
	ns := uint64(max(d, 0))
	sec, rem := ns/1e9, ns%1e9
	frac := (rem<<32 + 1e9 - 1) / 1e9
	var scale uint8
	// e = sec·2^32 + frac may exceed 64 bits; halve sec and frac together.
	for sec > 0 || frac > 255 {
		// ceil((sec·2^32 + frac) / 2) = sec/2·2^32 + ceil(((sec%2)·2^32 + frac) / 2)
		low := (sec%2)<<32 + frac
		sec /= 2
		frac = (low + 1) / 2
		scale++
	}
	return scale, uint8(max(frac, 1))
}

func TestSecondsAndDuration(t *testing.T) {
	tests := []struct {
		e   errest.Estimate
		sec float64
		dur time.Duration
	}{
		{errest.New(true, false, 32, 1), 1, time.Second},
		{errest.New(false, false, 0, 1), 1.0 / (1 << 32), 1}, // 0.23ns rounds up
		{errest.New(false, false, 10, 210), 210.0 / (1 << 22), 50068},
		{errest.New(false, false, 33, 255), 510, 510 * time.Second},
		{errest.New(false, false, 0, 0), 0, 0},
		{errest.Unknown, 255 * math.Pow(2, 31), math.MaxInt64},
		{errest.New(false, false, 56, 255), 255 * (1 << 24), 255 * (1 << 24) * time.Second},
		{errest.New(false, false, 57, 255), 255 * (1 << 25), 255 * (1 << 25) * time.Second},
		{errest.New(false, false, 58, 255), 255 * (1 << 26), math.MaxInt64},
	}
	for _, tt := range tests {
		if got := tt.e.Seconds(); got != tt.sec {
			t.Errorf("%v Seconds() = %g, want %g", tt.e, got, tt.sec)
		}
		if got := tt.e.Duration(); got != tt.dur {
			t.Errorf("%v Duration() = %v, want %v", tt.e, got, tt.dur)
		}
	}
}

func TestValid(t *testing.T) {
	if errest.New(true, false, 5, 0).Valid() {
		t.Error("multiplier 0 reported valid")
	}
	if !errest.New(false, false, 0, 1).Valid() {
		t.Error("multiplier 1 reported invalid")
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		e    errest.Estimate
		want string
	}{
		{errest.New(true, false, 32, 1), "S Z=0 1s"},
		{errest.New(false, true, 31, 1), "- Z=1 0.5s"},
	}
	for _, tt := range tests {
		if got := tt.e.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func FuzzEstimate(f *testing.F) {
	f.Add(uint16(0))
	f.Add(uint16(0xFFFF))
	f.Add(uint16(0x8AD2))
	f.Fuzz(func(t *testing.T, raw uint16) {
		e := errest.Estimate(raw)
		if got := errest.New(e.Synchronized(), e.PTP(), e.Scale(), e.Multiplier()); got != e {
			t.Fatalf("fields of %#04x rebuild to %#04x", raw, uint16(got))
		}
		if e.Duration() < 0 || e.Seconds() < 0 {
			t.Fatalf("%#04x decodes to a negative error", raw)
		}
		if !e.Valid() {
			return
		}
		// Re-encoding the decoded error never yields a smaller bound.
		d := e.Duration()
		if d == math.MaxInt64 {
			return
		}
		if back := errest.Compute(d, e.Synchronized()); back.Seconds() < e.Seconds() {
			t.Fatalf("Compute(%v) = %v is below %v", d, back, e)
		}
	})
}
