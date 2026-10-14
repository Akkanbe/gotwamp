//declscope:namespace accumulator // tests of the accumulator's working parts

package stats

import "testing"

func TestHopCounter(t *testing.T) {
	tests := []struct {
		name string
		add  []int
		want Hops
	}{
		{"empty", nil, Hops{}},
		{"one", []int{7}, Hops{Count: 1, Min: 7, Max: 7, Mode: 7}},
		{"mode", []int{5, 4, 5, 9, 5, 4}, Hops{Count: 6, Min: 4, Max: 9, Mode: 5}},
		{"tie takes smallest", []int{6, 3, 6, 3}, Hops{Count: 4, Min: 3, Max: 6, Mode: 3}},
		{"out of range ignored", []int{-1, 256, 0, 255}, Hops{Count: 2, Min: 0, Max: 255, Mode: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c hopCounter
			for _, h := range tt.add {
				c.add(h)
			}
			if got := c.hops(); got != tt.want {
				t.Errorf("hops() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
