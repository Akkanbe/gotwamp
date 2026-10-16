//declscope:namespace accumulator // the accumulator's working parts

package stats

import "github.com/Akkanbe/gotwamp"

// Hops is the canonical public value.
type Hops = gotwamp.Hops

// hopCounter is a histogram of hop counts.
type hopCounter [256]uint64

// add counts one packet; a negative hop count means unknown and is skipped.
func (c *hopCounter) add(hops int) {
	if hops < 0 || hops >= len(c) {
		return
	}
	c[hops]++
}

// hops summarizes the histogram.
func (c *hopCounter) hops() Hops {
	var h Hops
	var best uint64
	for v, n := range c {
		if n == 0 {
			continue
		}
		if h.Count == 0 {
			h.Min = v
		}
		h.Max = v
		h.Count += n
		if n > best {
			best = n
			h.Mode = v
		}
	}
	return h
}
