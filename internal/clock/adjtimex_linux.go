//go:build linux

package clock

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// adjtimex reads the kernel clock state with adjtimex(2) in read-only mode,
// which needs no privilege.
type adjtimex struct{}

// systemSource returns the kernel clock source of this platform.
//
//declscope:package // NewMonitor's default source
func systemSource() source { return adjtimex{} }

func (adjtimex) read() (bool, time.Duration, error) {
	var tx unix.Timex // Modes 0: read only
	state, err := unix.Adjtimex(&tx)
	if err != nil {
		return false, 0, fmt.Errorf("clock: adjtimex: %w", err)
	}
	synced, maxErr := fromTimex(state, tx.Status, int64(tx.Maxerror))
	return synced, maxErr, nil
}

// fromTimex interprets the result of adjtimex: the clock state, the status
// bits and maxerror in microseconds.
func fromTimex(state int, status int32, maxerrorUS int64) (bool, time.Duration) {
	synced := state != unix.TIME_ERROR && status&unix.STA_UNSYNC == 0
	if maxerrorUS < 0 || maxerrorUS > int64(maxErrorLimit/time.Microsecond) {
		return synced, maxErrorLimit
	}
	return synced, time.Duration(maxerrorUS) * time.Microsecond
}
