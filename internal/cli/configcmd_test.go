package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.yaml", "sender: {defaults: {padding: 1500}}\n")
	bad := write("bad.yaml", "version: 2\nreflector: {ttl: 0}\n")
	broken := write("broken.yaml", "daemon: [\n")
	one := write("one.yaml", "reflector: {ttl: 0}\n")

	code, out, _ := h.run(t.Context(), "config", "validate", "-f", good)
	if code != ExitOK || !strings.Contains(out, "warning: sender.defaults.padding") || !strings.Contains(out, "configuration is valid") {
		t.Errorf("good: %d %q", code, out)
	}
	code, _, errs := h.run(t.Context(), "config", "validate", "-f", bad)
	if code != ExitUsage || !strings.Contains(errs, "error: version:") || !strings.Contains(errs, "error: reflector.ttl:") ||
		!strings.Contains(errs, "(2 problems)") {
		t.Errorf("bad: %d %q", code, errs)
	}
	if code, _, errs := h.run(t.Context(), "config", "validate", "-f", one); code != ExitUsage || !strings.Contains(errs, "(1 problem)") {
		t.Errorf("one problem: %d %q", code, errs)
	}
	code, out, _ = h.run(t.Context(), "config", "validate", "-f", bad, "--json")
	var v validation
	if code != ExitUsage || json.Unmarshal([]byte(out), &v) != nil || v.Valid || len(v.Problems) != 2 {
		t.Errorf("bad --json: %d %q", code, out)
	}
	if code, _, errs := h.run(t.Context(), "config", "validate", "-f", broken); code != ExitUsage || !strings.Contains(errs, "yaml") {
		t.Errorf("broken: %d %q", code, errs)
	}
	if code, _, _ := h.run(t.Context(), "config", "validate", "-f", filepath.Join(dir, "missing.yaml")); code != ExitUsage {
		t.Errorf("missing: %d", code)
	}
}

// FuzzParseSessionFile: no session file makes the parser panic.
func FuzzParseSessionFile(f *testing.F) {
	f.Add([]byte("name: a\ntarget: x\nmode: light\ndscp: ef\nschedule: {type: continuous, window: 60s}\n"))
	f.Add([]byte("sender_port_range: 1-2\ninterval: 1s\nlabels: {a: b}\n"))
	f.Add([]byte("&a [*a, *a]"))
	f.Fuzz(func(_ *testing.T, b []byte) {
		_, _ = parseSessionFile(b)
	})
}
