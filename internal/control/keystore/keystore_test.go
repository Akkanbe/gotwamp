package keystore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/control/keystore"
)

// load writes content to a pfs file and loads it.
func load(t *testing.T, content string) (keystore.Map, string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "twamp-server.pfs")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := keystore.LoadPFS(path)
	return m, path, err
}

func TestLoadPFS(t *testing.T) {
	// alice's line is what perfSONAR writes for "secretpass".
	m, _, err := load(t, "# shared with perfSONAR\n\nalice 73656372657470617373\n  bob\t0102ff  # comment\n")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := m.Passphrase("alice"); !ok || string(p) != "secretpass" {
		t.Errorf("alice = %q, %v", p, ok)
	}
	if p, ok := m.Passphrase("bob"); !ok || string(p) != "\x01\x02\xff" {
		t.Errorf("bob = %x, %v", p, ok)
	}
	if _, ok := m.Passphrase("carol"); ok {
		t.Error("unknown identity found")
	}
	if _, err := keystore.LoadPFS(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing file loaded")
	}
}

func TestLoadPFSErrors(t *testing.T) {
	long := strings.Repeat("x", 81)
	for _, c := range []struct{ name, in, want string }{
		{"one field", "alice\n", "line 1: want an identity"},
		{"three fields", "alice 00 11\n", "line 1: want an identity"},
		{"not hex", "alice zz\n", `line 1: passphrase of "alice" is not hexadecimal`},
		{"odd hex", "alice 123\n", "not hexadecimal"},
		{"duplicate", "alice 01\nalice 02\n", `line 2: identity "alice" appears twice`},
		{"long identity", long + " 01\n", "line 1: identity"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, path, err := load(t, c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), path) {
				t.Errorf("err = %v, want %q and the path", err, c.want)
			}
		})
	}
	// The secret never shows in an error.
	_, _, err := load(t, "alice 01\nalice 7365637265740z\n")
	if err == nil || strings.Contains(err.Error(), "7365637265740z") {
		t.Errorf("err = %v", err)
	}
}
