//go:build !linux

package clock

import (
	"fmt"
	"time"
)

// unsupported is the clock source of platforms without adjtimex: the state is
// never known, so the clock is reported unsynchronized unless configured
// otherwise.
type unsupported struct{}

// systemSource returns the kernel clock source of this platform.
//
//declscope:package // NewMonitor's default source
func systemSource() source { return unsupported{} }

func (unsupported) read() (bool, time.Duration, error) {
	return false, 0, fmt.Errorf("clock: reading the clock state is supported on Linux only")
}
