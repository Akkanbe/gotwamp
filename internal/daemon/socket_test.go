package daemon

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/config"
)

// TestSocketGroup: the default group may be missing (the daemon keeps its
// own group, with a warning); a group the configuration names must exist
// (B12).
func TestSocketGroup(t *testing.T) {
	dir := t.TempDir()
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ln, err := listenUnix(filepath.Join(dir, "a.sock"), 0o660, config.DefaultSocketGroup, log)
	if err != nil {
		t.Fatalf("default group: %v", err)
	}
	_ = ln.Close()
	if _, err := listenUnix(filepath.Join(dir, "b.sock"), 0o660, "no-such-group-gotwamp-test", log); err == nil {
		t.Error("a missing configured group was accepted")
	}
	if config.Default().Daemon.API.SocketGroup != config.DefaultSocketGroup {
		t.Error("socket_group does not default to gotwamp (SPEC §9.2)")
	}
}
