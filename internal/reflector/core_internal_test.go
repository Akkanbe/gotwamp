//declscope:namespace core

package reflector

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestReadErrorsThinOut checks that a socket that keeps failing is logged
// once, then once per readErrorLogEvery with the count of the others.
func TestReadErrorsThinOut(t *testing.T) {
	var out bytes.Buffer
	r := readErrors{log: slog.New(slog.NewTextHandler(&out, nil))}
	for range 3 {
		r.failed("light reflector: read failed", errors.New("boom"), "listener", "x")
	}
	if n := strings.Count(out.String(), "read failed"); n != 1 || !strings.Contains(out.String(), "suppressed=0") {
		t.Fatalf("logged %d times:\n%s", n, out.String())
	}
	r.last = r.last.Add(-readErrorLogEvery)
	r.failed("light reflector: read failed", errors.New("boom"), "listener", "x")
	if n := strings.Count(out.String(), "read failed"); n != 2 || !strings.Contains(out.String(), "suppressed=2") {
		t.Errorf("after a minute, logged %d times:\n%s", n, out.String())
	}
	if r.skipped != 0 || time.Since(r.last) > time.Second {
		t.Errorf("state after logging: %+v", r)
	}
}
