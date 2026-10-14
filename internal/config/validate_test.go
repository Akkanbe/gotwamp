package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidTarget(t *testing.T) {
	good := []string{"192.0.2.1", "2001:db8::1", "fe80::1%eth0", "reflector.example", "reflector.example.", "a", "r_1.example", strings.Repeat("a", 63) + ".example"}
	for _, s := range good {
		if !validTarget(s) {
			t.Errorf("validTarget(%q) = false", s)
		}
	}
	bad := []string{"", " ", "host name", "[::1]", "192.0.2.1:862", "-host", "host-", "a..b", ".", "host/x", "ho\\x00st", "höst", strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 127) + "aa"}
	for _, s := range bad {
		if validTarget(s) {
			t.Errorf("validTarget(%q) = true", s)
		}
	}
}

func TestUpperBounds(t *testing.T) {
	_, err := Parse([]byte(`
daemon:
  limits: {max_concurrent_tests: 0, max_watchers: 5000}
sender:
  enabled: true
  defaults: {interval: 2h, timeout: 90m, control_timeout: 2h, end_delay: 2h, tx_timestamp_wait: 2s}
  sessions:
    - {name: a, target: "bad target", count: 1000000, schedule: {frequency: 48h, jitter: 25h}, labels: {site: "` + strings.Repeat("x", 300) + `"}}
    - {name: b, target: x, schedule: {type: continuous, window: 25h}}
`))
	want := []string{
		"daemon.limits.max_concurrent_tests:", "daemon.limits.max_watchers:",
		"sender.defaults.interval: 2h0m0s exceeds 1h0m0s", "sender.defaults.timeout:", "sender.defaults.control_timeout:",
		"sender.defaults.end_delay:", "sender.defaults.tx_timestamp_wait:",
		`sender.sessions[0].target: "bad target" is neither`, "sender.sessions[0].schedule.frequency: 48h0m0s exceeds",
		"sender.sessions[0].schedule.jitter:", "sender.sessions[0].labels.site: label value",
		"sender.sessions[1].schedule.window: 25h0m0s exceeds",
	}
	if err == nil {
		t.Fatal("accepted")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in:\n%v", w, err)
		}
	}
}

func TestRunLengthSaturates(t *testing.T) {
	if got := runLength(3, time.Second, time.Second); got != 5*time.Second {
		t.Errorf("runLength = %v", got)
	}
	if got := runLength(1_000_000, 1<<62, time.Second); got <= 0 {
		t.Errorf("runLength overflowed to %v", got)
	}
	if got := runLength(-1, time.Second, 0); got != 0 {
		t.Errorf("runLength(negative) = %v", got)
	}
}

func TestLoadWarnsWorldWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0].String(), "writable by every user") {
		t.Errorf("warnings = %v", w)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(path); len(c.Warnings()) != 0 {
		t.Errorf("warnings for 0640 = %v", c.Warnings())
	}
}

// FuzzParse: no document makes Parse panic, and whatever it accepts
// validates again and has a session list in step with its names.
func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nsender: {enabled: true}\n"))
	f.Add([]byte(""))
	f.Add([]byte("sender:\n  enabled: true\n  sessions:\n    - {name: a, target: x, schedule: {frequency: 60s}}\n"))
	f.Add([]byte("daemon: {api: {socket_mode: \"0777\"}, metrics: {rtt_buckets: [1ms, 1ms]}}\n"))
	f.Add([]byte("export: {jsonl: {rotate: {max_size: 99999999999GiB}}}\n"))
	f.Add([]byte("sender: {defaults: {dscp: af41, interval: -1s}}\nreflector: {light: {acl: [\"::/0\"]}}\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Parse(b)
		if err != nil {
			return
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("accepted a configuration that does not validate again: %v", err)
		}
	})
}

// TestControlServerValues: max_padding 0 and a pbkdf2_count that is no
// power of 2 are refused at load (B13, B14); every label name the exporter
// uses is reserved (B29).
func TestControlServerValues(t *testing.T) {
	_, err := Parse([]byte(`
reflector:
  control: {max_padding: 0, pbkdf2_count: 3000}
sender:
  enabled: true
  sessions:
    - {name: a, target: x, labels: {stage: s, kind: k}}
`))
	if err == nil {
		t.Fatal("accepted")
	}
	for _, w := range []string{
		"reflector.control.max_padding: 0 is out of range", "reflector.control.pbkdf2_count: 3000 is not a power of 2",
		"sender.sessions[0].labels.stage: label name is reserved", "sender.sessions[0].labels.kind: label name is reserved",
	} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in:\n%v", w, err)
		}
	}
}

// TestNewTestKeys: reflector_sequence takes stateful or copy_sender, and
// RFC 7750 monitoring needs Light (B32).
func TestNewTestKeys(t *testing.T) {
	c, err := Parse([]byte(`
daemon: {clock: {kernel_error: 3us}}
sender:
  enabled: true
  defaults: {reflector_sequence: copy_sender}
  sessions:
    - {name: a, target: x, mode: light, dscp_ecn_monitoring: true, schedule: {frequency: 1m}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if s := c.Sender.Sessions[0]; s.ReflectorSequence != "copy_sender" || !s.DSCPECNMonitoring || c.Daemon.Clock.KernelError != 3*time.Microsecond {
		t.Errorf("parsed %+v, kernel_error %v", s.TestParams, c.Daemon.Clock.KernelError)
	}
	_, err = Parse([]byte(`
sender:
  defaults: {reflector_sequence: counter}
  sessions:
    - {name: a, target: x, mode: full, dscp_ecn_monitoring: true}
`))
	for _, w := range []string{"sender.defaults.reflector_sequence:", "sender.sessions[0].dscp_ecn_monitoring: is supported in light mode only"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
}

// TestAPITCP: the API over TCP needs the three TLS files as absolute paths
// (ADR 0010); TLS settings without a listen address are only warned of.
func TestAPITCP(t *testing.T) {
	_, err := Parse([]byte(`
daemon:
  api: {tcp: {listen: "0.0.0.0:9443", ca: ca.pem, allowed_names: [""]}}
`))
	for _, w := range []string{
		"daemon.api.tcp.ca: \"ca.pem\" is not an absolute path", "daemon.api.tcp.cert: is required: the API is served over TCP with mutual TLS only",
		"daemon.api.tcp.key: is required", "daemon.api.tcp.allowed_names[0]: is empty",
	} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
	if _, err := Parse([]byte(`daemon: {api: {tcp: {listen: "host:9443", ca: /a, cert: /b, key: /c}}}`)); err == nil ||
		!strings.Contains(err.Error(), "daemon.api.tcp.listen") {
		t.Errorf("host name listen: %v", err)
	}
	c, err := Parse([]byte(`daemon: {api: {tcp: {listen: "127.0.0.1:9443", ca: /a, cert: /b, key: /c}}}`))
	if err != nil || !hasWarning(c, "daemon.api.tcp.allowed_names") {
		t.Errorf("no allowed names: %v, warnings %v", err, c.Warnings())
	}
	c, err = Parse([]byte(`daemon: {api: {tcp: {ca: /a}}}`))
	if err != nil || !hasWarning(c, "daemon.api.tcp") {
		t.Errorf("TLS without listen: %v, warnings %v", err, c.Warnings())
	}
}

// TestSecretFileModes: Load refuses a private key or a keys file others may
// read, and one that does not exist.
func TestSecretFileModes(t *testing.T) {
	dir := t.TempDir()
	secret := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // past the umask
			t.Fatal(err)
		}
		return p
	}
	ok, open := secret("ok.key", 0o600), secret("open.key", 0o640)
	load := func(doc string) error {
		p := filepath.Join(dir, "gotwampd.yaml")
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		return err
	}
	if err := load("daemon: {api: {tcp: {listen: \"127.0.0.1:9443\", ca: " + ok + ", cert: " + ok + ", key: " + ok + "}}}\nreflector: {control: {keys_file: " + ok + "}}\n"); err != nil {
		t.Errorf("0600 files: %v", err)
	}
	err := load("daemon: {api: {tcp: {listen: \"127.0.0.1:9443\", ca: " + ok + ", cert: " + ok + ", key: " + open + "}}}\n" +
		"sender: {keys_file: " + filepath.Join(dir, "none") + "}\nreflector: {control: {keys_file: " + open + "}}\n")
	for _, w := range []string{"daemon.api.tcp.key: " + open + " has mode -rw-r-----", "sender.keys_file:", "reflector.control.keys_file: " + open} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
}

// TestSecurityKeys: a mode other than unauthenticated needs a KeyID and
// sender.keys_file, and is warned of as not implemented yet.
func TestSecurityKeys(t *testing.T) {
	_, err := Parse([]byte(`
sender:
  enabled: true
  defaults: {security: authenticated, key_id: "` + strings.Repeat("k", 81) + `"}
  sessions:
    - {name: a, target: x, security: tls, schedule: {frequency: 1m}}
`))
	for _, w := range []string{
		"sender.defaults.key_id: is not UTF-8 of at most 80 octets", "sender.keys_file: is required with security authenticated",
		`sender.sessions[0].security: "tls" is not one of`,
	} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
	_, err = Parse([]byte(`
sender:
  enabled: true
  keys_file: /etc/gotwamp/keys.pfs
  sessions:
    - {name: a, target: x, security: mixed, key_id: alice, schedule: {frequency: 1m}}
    - {name: b, target: x, security: encrypted, schedule: {frequency: 1m}}
`))
	if err == nil || !strings.Contains(err.Error(), "sender.sessions[1].key_id: is required with security encrypted") {
		t.Errorf("no key id: %v", err)
	}
	c, err := Parse([]byte(`
sender:
  enabled: true
  keys_file: /etc/gotwamp/keys.pfs
  sessions:
    - {name: a, target: x, security: mixed, key_id: alice, schedule: {frequency: 1m}}
`))
	if err != nil || len(c.Warnings()) != 0 {
		t.Errorf("mixed with a key: %v, warnings %v", err, c.Warnings())
	}
	// TWAMP Light is unauthenticated only: a Light session asking for a
	// mode is refused, one inheriting it from the defaults ignores it.
	_, err = Parse([]byte(`
sender:
  enabled: true
  keys_file: /etc/gotwamp/keys.pfs
  defaults: {security: authenticated, key_id: alice}
  sessions:
    - {name: a, target: x, mode: light, schedule: {frequency: 1m}}
    - {name: b, target: x, mode: light, security: encrypted, schedule: {frequency: 1m}}
`))
	if err == nil || strings.Contains(err.Error(), "sender.sessions[0]") || !strings.Contains(err.Error(), "sender.sessions[1].security: TWAMP Light is unauthenticated only") {
		t.Errorf("light with security: %v", err)
	}
	if _, err := Parse([]byte("sender: {keys_file: /k, defaults: {mode: light, security: mixed, key_id: a}}\n")); err == nil ||
		!strings.Contains(err.Error(), "sender.defaults.security: TWAMP Light") {
		t.Errorf("light defaults with security: %v", err)
	}
}

// TestControlModes: the modes the control server offers, and their keys.
func TestControlModes(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want []string
	}{
		{"reflector: {control: {}}", []string{"unauthenticated"}},
		{"reflector: {control: {keys_file: /k}}", []string{"unauthenticated", "authenticated", "encrypted", "mixed"}},
		{"reflector: {control: {keys_file: /k, modes: [encrypted]}}", []string{"encrypted"}},
	} {
		c, err := Parse([]byte(tc.doc))
		if err != nil || !slices.Equal(c.Reflector.Control.OfferedModes(), tc.want) {
			t.Errorf("%s: %v, %v", tc.doc, err, c)
		}
	}
	_, err := Parse([]byte("reflector: {control: {modes: [authenticated, tls, authenticated]}}"))
	for _, w := range []string{
		"modes[0]: authenticated needs reflector.control.keys_file", `modes[1]: "tls" is not one of`, `modes[2]: "authenticated" is listed twice`,
	} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
	c, err := Parse([]byte("reflector: {control: {keys_file: /k, modes: [unauthenticated]}}"))
	if err != nil || !hasWarning(c, "reflector.control.keys_file") {
		t.Errorf("unused keys: %v, %v", err, c.Warnings())
	}
}

// TestHardwareKeys: daemon.hardware_interface and clock.hardware_error.
func TestHardwareKeys(t *testing.T) {
	c, err := Parse([]byte("daemon: {timestamping: hardware, hardware_interface: eth0, clock: {hardware_error: 2us}}"))
	if err != nil || c.Daemon.HardwareInterface != "eth0" || c.Daemon.Clock.HardwareError != 2*time.Microsecond || len(c.Warnings()) != 0 {
		t.Errorf("hardware: %v, %+v, %v", err, c, c.Warnings())
	}
	if Default().Daemon.Clock.HardwareError != time.Microsecond {
		t.Error("hardware_error default is not 1us")
	}
	c, err = Parse([]byte("daemon: {hardware_interface: eth0}"))
	if err != nil || !hasWarning(c, "daemon.hardware_interface") {
		t.Errorf("interface without hardware: %v, %v", err, c.Warnings())
	}
	if _, err := Parse([]byte("daemon: {timestamping: hardware, hardware_interface: \"a/b\", clock: {hardware_error: -1us}}")); err == nil ||
		!strings.Contains(err.Error(), "daemon.hardware_interface") || !strings.Contains(err.Error(), "daemon.clock.hardware_error") {
		t.Errorf("bad hardware keys: %v", err)
	}
	c, err = Parse([]byte("export: {jsonl: {enabled: true, rotate: {daily: true}}}"))
	if err != nil || !c.Export.JSONL.Rotate.Daily {
		t.Errorf("daily: %v", err)
	}
}

func hasWarning(c *Config, path string) bool {
	if c == nil {
		return false
	}
	for _, w := range c.Warnings() {
		if w.Path == path {
			return true
		}
	}
	return false
}

// TestReflectorWithoutListeners: a reflector role with no address to listen
// on is accepted, with a warning, since it reflects nothing.
func TestReflectorWithoutListeners(t *testing.T) {
	c, err := Parse([]byte("reflector: {control: {listen: []}, light: {listeners: []}}\n"))
	if err != nil || !hasWarning(c, "reflector") {
		t.Errorf("no listeners: %v, warnings %v", err, c.Warnings())
	}
	if c, err := Parse([]byte("reflector: {control: {listen: []}}\n")); err != nil || hasWarning(c, "reflector") {
		t.Errorf("Light listeners only: %v, warnings %v", err, c.Warnings())
	}
}

// TestExtensionKeys: the TWAMP extensions of Full tests, STAMP of Light
// ones, and what the reflector offers.
func TestExtensionKeys(t *testing.T) {
	c, err := Parse([]byte(`
sender:
  enabled: true
  defaults: {extensions: [individual_session_control, symmetrical_size], octets_to_reflect: 2}
  sessions:
    - {name: full, target: x, schedule: {frequency: 1m}}
    - {name: light, target: x, mode: light, stamp: true, ssid: 7, schedule: {frequency: 1m}}
reflector: {light: {stamp: true}}
`))
	if err != nil {
		t.Fatal(err)
	}
	full, light := c.Sender.Sessions[0], c.Sender.Sessions[1]
	if len(full.Extensions) != 2 || full.OctetsToReflect != 2 || len(light.Extensions) != 0 || light.OctetsToReflect != 0 ||
		!light.STAMP || light.SSID != 7 || !c.Reflector.Light.STAMP {
		t.Errorf("parsed: full %+v, light %+v", full.TestParams, light.TestParams)
	}
	if got := Default().Reflector.Control.Extensions; !slices.Equal(got, []string{"individual_session_control", "reflect_octets", "symmetrical_size"}) {
		t.Errorf("offered by default: %v", got)
	}
	_, err = Parse([]byte(`
sender:
  enabled: true
  defaults: {mode: light}
  sessions:
    - {name: a, target: x, mode: light, extensions: [reflect_octets], schedule: {frequency: 1m}}
    - {name: b, target: x, mode: full, stamp: true, schedule: {frequency: 1m}}
    - {name: c, target: x, mode: light, ssid: 9, stamp: false, schedule: {frequency: 1m}}
    - {name: d, target: x, mode: full, extensions: [tls, tls], octets_to_reflect: 70000, schedule: {frequency: 1m}}
    - {name: e, target: x, mode: light, stamp: true, padding: 2, schedule: {frequency: 1m}}
    - {name: f, target: x, mode: light, stamp: true, dscp_ecn_monitoring: true, schedule: {frequency: 1m}}
reflector: {control: {extensions: [stamp]}, light: {stamp: true, dscp_ecn_monitoring: true}}
`))
	for _, w := range []string{
		"sender.sessions[0].extensions: does not apply to light mode", "sender.sessions[1].stamp: does not apply to full mode",
		"sender.sessions[2].ssid: needs stamp", `sender.sessions[3].extensions[0]: "tls" is not one of`,
		`sender.sessions[3].extensions[1]: "tls" is listed twice`, "sender.sessions[3].octets_to_reflect: 70000 is out of range",
		"sender.sessions[4].padding: 2 is shorter than the 4-octet STAMP", "sender.sessions[5].stamp: excludes dscp_ecn_monitoring",
		`reflector.control.extensions[0]: "stamp" is not one of`, "reflector.light.stamp: excludes dscp_ecn_monitoring",
	} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
}

// TestStoreKeys: the result store.
func TestStoreKeys(t *testing.T) {
	c, err := Parse([]byte("store: {backend: sqlite, sqlite: {path: /var/lib/x.db, retention: 0s, max_size: 1GiB, on_corrupt: fail}}"))
	if err != nil || c.Store.Backend != "sqlite" || c.Store.SQLite.Path != "/var/lib/x.db" || c.Store.SQLite.Retention != 0 ||
		c.Store.SQLite.MaxSize != 1<<30 || c.Store.SQLite.OnCorrupt != "fail" {
		t.Errorf("sqlite: %v, %+v", err, c.Store)
	}
	if d := Default().Store; d.Backend != "memory" || d.SQLite.Retention != 30*24*time.Hour || d.SQLite.OnCorrupt != "reset" {
		t.Errorf("defaults: %+v", d)
	}
	_, err = Parse([]byte("store: {backend: sqlite, sqlite: {path: x.db, retention: -1h, on_corrupt: ignore}}"))
	for _, w := range []string{"store.sqlite.path", "store.sqlite.retention", "store.sqlite.on_corrupt"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("missing %q in: %v", w, err)
		}
	}
	if _, err := Parse([]byte("store: {backend: redis}")); err == nil || !strings.Contains(err.Error(), "store.backend") {
		t.Errorf("unknown backend: %v", err)
	}
	// The daemon keeps the session definitions as JSON: they must come back.
	c, err = Parse([]byte(`
sender:
  enabled: true
  sessions:
    - {name: a, target: x, labels: {site: t}, extensions: [symmetrical_size], schedule: {frequency: 1m, jitter: 5s}}
`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c.Sender.Sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, c.Sender.Sessions[0]) {
		t.Errorf("JSON round trip: %v\n got %+v\nwant %+v", err, back, c.Sender.Sessions[0])
	}
}
