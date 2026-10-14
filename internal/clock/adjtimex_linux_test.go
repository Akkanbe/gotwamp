//go:build linux

package clock

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestFromTimex(t *testing.T) {
	cases := []struct {
		name       string
		state      int
		status     int32
		maxerrorUS int64
		wantSynced bool
		wantMaxErr time.Duration
	}{
		{"synced", unix.TIME_OK, 0, 1500, true, 1500 * time.Microsecond},
		{"leap insert pending", unix.TIME_INS, 0x0010, 20, true, 20 * time.Microsecond},
		{"STA_UNSYNC", unix.TIME_OK, unix.STA_UNSYNC, 16_000_000, false, 16 * time.Second},
		{"TIME_ERROR", unix.TIME_ERROR, 0, 100, false, 100 * time.Microsecond},
		{"maxerror above limit", unix.TIME_OK, 0, 17_000_000, true, maxErrorLimit},
		{"negative maxerror", unix.TIME_OK, 0, -1, true, maxErrorLimit},
	}
	for _, tc := range cases {
		synced, maxErr := fromTimex(tc.state, tc.status, tc.maxerrorUS)
		if synced != tc.wantSynced || maxErr != tc.wantMaxErr {
			t.Errorf("%s: fromTimex = %v, %v; want %v, %v", tc.name, synced, maxErr, tc.wantSynced, tc.wantMaxErr)
		}
	}
}

func TestAdjtimexReadsTheKernel(t *testing.T) {
	_, maxErr, err := systemSource().read()
	if errors.Is(err, unix.EPERM) {
		t.Skipf("adjtimex is not permitted here: %v", err)
	}
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if maxErr < 0 || maxErr > maxErrorLimit {
		t.Errorf("maxErr = %v, want within [0, %v]", maxErr, maxErrorLimit)
	}
}
