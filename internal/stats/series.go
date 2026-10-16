//declscope:namespace accumulator // the accumulator's working parts

package stats

import (
	"math"
	"slices"
	"time"

	"github.com/Akkanbe/gotwamp"
)

// Distribution is the canonical public value.
type Distribution = gotwamp.Distribution

// series accumulates one metric: exact moments and extremes over every value,
// and an equally spaced subset of at most limit values for the percentiles.
// When the subset fills, every other value is dropped and the stride doubles,
// so the kept values are those whose index is a multiple of stride.
type series struct {
	n       uint64
	mean    float64
	m2      float64
	min     time.Duration
	max     time.Duration
	samples []time.Duration
	limit   int
	stride  uint64
}

// newSeries returns an empty series keeping at most limit samples.
func newSeries(limit int) *series {
	return &series{limit: max(limit, 1), stride: 1}
}

// add observes one value.
func (s *series) add(d time.Duration) {
	i := s.n
	s.n++
	if i == 0 || d < s.min {
		s.min = d
	}
	if i == 0 || d > s.max {
		s.max = d
	}
	x := float64(d)
	delta := x - s.mean
	s.mean += delta / float64(s.n)
	s.m2 += delta * (x - s.mean)

	if i%s.stride != 0 {
		return
	}
	if len(s.samples) == s.limit {
		kept := s.samples[:0]
		for k := 0; k < len(s.samples); k += 2 {
			kept = append(kept, s.samples[k])
		}
		s.samples = kept
		s.stride *= 2
		if i%s.stride != 0 || len(s.samples) == s.limit {
			return
		}
	}
	s.samples = append(s.samples, d)
}

// clone returns an independent copy of s.
func (s *series) clone() *series {
	c := *s
	c.samples = slices.Clone(s.samples)
	return &c
}

// distribution summarizes the values observed so far.
func (s *series) distribution() Distribution {
	if s.n == 0 {
		return Distribution{}
	}
	d := Distribution{
		Count:   s.n,
		Samples: len(s.samples),
		Min:     s.min,
		Max:     s.max,
		Mean:    time.Duration(math.Round(s.mean)),
	}
	if s.n > 1 {
		d.StdDev = time.Duration(math.Round(math.Sqrt(s.m2 / float64(s.n-1))))
	}
	sorted := slices.Clone(s.samples)
	slices.Sort(sorted)
	d.Median = percentile(sorted, 0.50)
	d.P90 = percentile(sorted, 0.90)
	d.P95 = percentile(sorted, 0.95)
	d.P99 = percentile(sorted, 0.99)
	d.P999 = percentile(sorted, 0.999)
	return d
}

// percentile returns the p-quantile (0 ≤ p ≤ 1) of sorted by linear
// interpolation between the closest ranks (Hyndman and Fan type 7, the
// default of R, NumPy and Excel PERCENTILE.INC): with h = (n − 1)·p, the
// result is x[⌊h⌋] + (h − ⌊h⌋)·(x[⌊h⌋+1] − x[⌊h⌋]). The median of an even
// number of values is therefore the mean of the two middle ones.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	h := p * float64(len(sorted)-1)
	lo := int(math.Floor(h))
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := h - float64(lo)
	return sorted[lo] + time.Duration(math.Round(frac*float64(sorted[lo+1]-sorted[lo])))
}

// shift returns d with every location statistic reduced by base. It turns
// the RTT distribution into PDV, rtt − min(rtt) (RFC 5481 §4.2); the spread
// is unchanged.
func shift(d Distribution, base time.Duration) Distribution {
	if d.Count == 0 {
		return d
	}
	d.Min -= base
	d.Max -= base
	d.Mean -= base
	d.Median -= base
	d.P90 -= base
	d.P95 -= base
	d.P99 -= base
	d.P999 -= base
	return d
}
