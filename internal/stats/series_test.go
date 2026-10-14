//declscope:namespace accumulator // tests of the accumulator's working parts

package stats

import (
	"slices"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	// Linear interpolation between closest ranks: h = (n − 1)·p.
	four := []time.Duration{10, 20, 30, 40}
	tests := []struct {
		name   string
		sorted []time.Duration
		p      float64
		want   time.Duration
	}{
		{"empty", nil, 0.5, 0},
		{"single", []time.Duration{5}, 0.99, 5},
		{"min", four, 0, 10},
		{"max", four, 1, 40},
		{"even median averages the middle", four, 0.5, 25},
		{"h = 0.75", four, 0.25, 18}, // 10 + 0.75·10 = 17.5, rounded half away from zero
		{"h = 2.7", four, 0.9, 37},
		{"odd median is the middle", []time.Duration{1, 2, 100}, 0.5, 2},
		{"p95 of 1..100", seq(1, 100), 0.95, 9505}, // h = 94.05 → 95 + 0.05·1, in hundredths
		{"p99.9 of 1..1000", seq(1, 1000), 0.999, 99900},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.sorted, tt.p); got != tt.want {
				t.Errorf("percentile(%v) = %d, want %d", tt.p, got, tt.want)
			}
		})
	}
}

// seq returns 100·lo, 100·(lo+1), …, 100·hi.
func seq(lo, hi int) []time.Duration {
	var s []time.Duration
	for v := lo; v <= hi; v++ {
		s = append(s, time.Duration(100*v))
	}
	return s
}

func TestSeriesDecimation(t *testing.T) {
	s := newSeries(4)
	for i := range 10 {
		s.add(time.Duration(i))
	}
	// Full at index 4: keep 0, 2, then 4 and 6 at stride 2. Full again at
	// index 8: keep 0, 4, then 8 at stride 4.
	if want := []time.Duration{0, 4, 8}; !slices.Equal(s.samples, want) || s.stride != 4 {
		t.Errorf("samples %v stride %d, want %v stride 4", s.samples, s.stride, want)
	}
	d := s.distribution()
	if d.Count != 10 || d.Samples != 3 || d.Min != 0 || d.Max != 9 || d.Median != 4 {
		t.Errorf("distribution = %+v", d)
	}

	one := newSeries(0)
	for i := range 20 {
		one.add(time.Duration(i))
	}
	if len(one.samples) != 1 || one.samples[0] != 0 {
		t.Errorf("limit 1: samples %v", one.samples)
	}
}

func TestSeriesClone(t *testing.T) {
	s := newSeries(8)
	s.add(1)
	c := s.clone()
	c.add(100)
	if s.n != 1 || len(s.samples) != 1 || s.max != 1 {
		t.Errorf("clone shares state: %+v", s)
	}
	if c.n != 2 || c.max != 100 {
		t.Errorf("clone = %+v", c)
	}
}

func TestDistributionMoments(t *testing.T) {
	s := newSeries(16)
	for _, v := range []time.Duration{2, 4, 4, 4, 5, 5, 7, 9} {
		s.add(v * time.Millisecond)
	}
	d := s.distribution()
	// Mean 5 ms; the squared deviations sum to 32 ms², so the sample
	// standard deviation is √(32/7) ms.
	if d.Mean != 5*time.Millisecond || d.StdDev != 2138090 {
		t.Errorf("distribution = %+v", d)
	}
	if got := (Distribution{}).shift(time.Second); got != (Distribution{}) {
		t.Errorf("shift of empty = %+v", got)
	}
}
