//declscope:namespace accumulator // the accumulator's working parts

package stats

// Hops summarizes the hop counts of one direction over a run or window.
// When Count is zero the other fields are zero and undefined.
type Hops struct {
	// Count is the number of packets with a known hop count.
	Count uint64
	// Min is the smallest hop count observed.
	Min int
	// Max is the largest hop count observed.
	Max int
	// Mode is the most frequent hop count, the smallest one on a tie.
	Mode int
}

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
