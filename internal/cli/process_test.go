package cli

import (
	"testing"
)

// TestRun: the process entry point runs a command with the standard
// streams and reports its exit status.
func TestRun(t *testing.T) {
	if code := Run([]string{"version"}, BuildInfo{Version: "v1"}); code != ExitOK {
		t.Errorf("version: exit %d", code)
	}
	if code := Run([]string{"--socket", t.TempDir() + "/none.sock", "status"}, BuildInfo{}); code != ExitUnavailable {
		t.Errorf("status without a daemon: exit %d, want %d", code, ExitUnavailable)
	}
}
