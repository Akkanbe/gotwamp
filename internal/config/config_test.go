package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// specExample is the complete example of SPEC §9.2.
const specExample = `
version: 1

daemon:
  api:
    socket: /run/gotwamp/gotwampd.sock
    socket_mode: "0660"
    socket_group: gotwamp
  metrics:
    listen: "127.0.0.1:9862"
    rtt_buckets: [50us, 100us, 200us, 500us, 1ms, 2ms, 5ms, 10ms, 20ms, 50ms, 100ms, 200ms, 500ms, 1s]
  log:
    level: info
    format: json
  timestamping: auto
  clock:
    sync: auto
    max_error: 0s
    userspace_error: 50us
  realtime:
    lock_os_thread: false
  limits:
    max_samples_per_run: 100000
    result_history: 1440

sender:
  enabled: true
  defaults:
    mode: full
    control_port: 862
    receiver_port: 0
    sender_port: 0
    sender_port_range: "49152-57343"
    count: 100
    interval: 100ms
    timeout: 2s
    padding: 27
    padding_fill: auto
    dscp: 0
    ttl: 255
    control_timeout: 15s
    control_dscp: 0
    end_delay: 0s
    zero_addresses: false
    start_time: zero
    accept_alternate_port: true
    tolerate_zero_multiplier: true
    tx_timestamp_wait: 1ms
  sessions:
    - name: tokyo-osaka
      target: 192.0.2.10
      ipv: auto
      source: ""
      mode: full
      schedule:
        type: periodic
        frequency: 60s
        jitter: 5s
      count: 100
      interval: 100ms
      dscp: ef
      labels: {site: tokyo, link: primary}
    - name: tokyo-osaka-light
      target: 2001:db8::10
      mode: light
      receiver_port: 862
      sender_port: 50000
      schedule:
        type: continuous
        window: 60s
      interval: 1s
      padding: 27

reflector:
  enabled: true
  control:
    listen: ["0.0.0.0:862", "[::]:862"]
    acl: []
    max_connections: 64
    max_sessions_per_connection: 16
    max_sessions: 1024
    servwait: 900s
    refwait: 900s
    partial_message_timeout: 60s
    test_port_range: "20000-29999"
    honor_requested_port: true
    verify_sender: address
    max_padding: 9000
    pbkdf2_count: 2048
  light:
    listeners:
      - listen: "0.0.0.0:862"
      - listen: "[::]:862"
    acl: []
    sequence: per_flow
    flow_timeout: 900s
    max_flows: 65536
    rate_limit_pps: 0
    dscp_policy: copy
    dscp_fixed: 0
  ttl: 255
  max_packet_size: 9000

export:
  jsonl:
    enabled: false
    path: /var/lib/gotwamp/results.jsonl
    records: [run_summary, window_summary, run_error]
    rotate:
      max_size: 100MiB
      max_files: 10
`

func TestSpecExample(t *testing.T) {
	c, err := Parse([]byte(specExample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Sender.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(c.Sender.Sessions))
	}
	s0, s1 := c.Sender.Sessions[0], c.Sender.Sessions[1]
	if s0.DSCP != 46 || s0.Labels["site"] != "tokyo" || s0.Schedule.Frequency != time.Minute {
		t.Errorf("session 0 = %+v", s0)
	}
	// Session 1 leaves count, timeout, ttl and others to sender.defaults.
	if s1.Mode != "light" || s1.Interval != time.Second || s1.Count != 100 || s1.Timeout != 2*time.Second ||
		s1.TTL != 255 || s1.SenderPortRange != "49152-57343" || !s1.AcceptAlternatePort {
		t.Errorf("session 1 did not inherit the defaults: %+v", s1)
	}
	if c.Export.JSONL.Rotate.MaxSize != 100<<20 {
		t.Errorf("max_size = %d", c.Export.JSONL.Rotate.MaxSize)
	}
	if len(c.Warnings()) != 0 {
		t.Errorf("warnings = %v", c.Warnings())
	}
	// The example states every default, so apart from the sessions and the
	// sender role it equals Default.
	d := Default()
	c.Sender.Enabled, c.Sender.Sessions = false, nil
	c.Reflector.Control.ACL, c.Reflector.Light.ACL, c.warnings = nil, nil, nil // "[]" in the example
	if !reflect.DeepEqual(c, d) {
		t.Errorf("SPEC example differs from Default:\n got %+v\nwant %+v", c, d)
	}
}

func TestEmptyDocumentIsDefault(t *testing.T) {
	for _, doc := range []string{"", "# nothing\n", "version: 1\n"} {
		c, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("Parse(%q): %v", doc, err)
		}
		if !reflect.DeepEqual(c, Default()) {
			t.Errorf("Parse(%q) differs from Default", doc)
		}
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("Default().Validate() = %v", err)
	}
}

func TestDefaultsInheritance(t *testing.T) {
	c, err := Parse([]byte(`
sender:
  enabled: true
  defaults:
    mode: light
    interval: 10ms
    dscp: af41
    padding: 100
  sessions:
    - name: a
      target: 192.0.2.1
      schedule: {frequency: 10s}
    - name: b
      target: 192.0.2.2
      padding: 0
      dscp: 0
      accept_alternate_port: false
      schedule: {frequency: 10s}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	a, b := c.Sender.Sessions[0], c.Sender.Sessions[1]
	if a.Mode != "light" || a.Interval != 10*time.Millisecond || a.DSCP != 34 || a.Padding != 100 || a.Count != 100 {
		t.Errorf("a = %+v", a.TestParams)
	}
	// Explicit zero values override non-zero defaults.
	if b.Padding != 0 || b.DSCP != 0 || b.AcceptAlternatePort || b.Mode != "light" {
		t.Errorf("b = %+v", b.TestParams)
	}
	if !c.Reflector.Enabled {
		t.Error("reflector.enabled should keep its default (true)")
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want []string // substrings of problems, each must appear
	}{
		{"version", "version: 2", []string{"version: 2 is not supported"}},
		{"no role", "reflector: {enabled: false}", []string{"neither sender.enabled nor reflector.enabled"}},
		{"enums", `
daemon:
  log: {level: loud, format: xml}
  timestamping: ptp
  clock: {sync: maybe}
`, []string{"daemon.log.level:", "daemon.log.format:", "daemon.timestamping:", "daemon.clock.sync:"}},
		{"socket", `daemon: {api: {socket: relative.sock, socket_mode: "999"}}`, []string{"daemon.api.socket:", "daemon.api.socket_mode:"}},
		{"socket path length", "daemon: {api: {socket: /" + strings.Repeat("s", 107) + "}}", []string{"daemon.api.socket: path is 108 bytes long"}},
		{"metrics", `daemon: {metrics: {listen: "nope", rtt_buckets: [2ms, 1ms]}}`, []string{"daemon.metrics.listen:", "daemon.metrics.rtt_buckets[1]:"}},
		{"defaults", `
sender:
  defaults:
    mode: half
    count: 0
    interval: 500us
    ttl: 0
    padding: 9001
    sender_port_range: "60000-50000"
    padding_fill: ones
    start_time: later
`, []string{
			"sender.defaults.mode:", "sender.defaults.count:", "sender.defaults.interval:", "sender.defaults.ttl:",
			"sender.defaults.padding:", "sender.defaults.sender_port_range:", "sender.defaults.padding_fill:", "sender.defaults.start_time:",
		}},
		{"sessions", `
sender:
  enabled: true
  sessions:
    - {name: Bad Name, target: "", schedule: {frequency: 1s}}
    - {name: dup, target: x, ipv: "5", source: nope, schedule: {frequency: 60s}}
    - {name: dup, target: x, schedule: {type: continuous, window: 5s}, interval: 1s}
    - {name: lbl, target: x, schedule: {type: sometimes}, labels: {session: x, "9bad": y}}
`, []string{
			"sender.sessions[0].name:", "sender.sessions[0].target: is required",
			"sender.sessions[0].schedule.frequency: 1s is shorter than count × interval + timeout + 1s = 13s",
			"sender.sessions[1].ipv:", "sender.sessions[1].source:",
			`sender.sessions[2].name: "dup" is also the name of sender.sessions[1]`,
			"sender.sessions[2].schedule.window: 5s holds fewer than 10 intervals",
			"sender.sessions[3].schedule.type:", "sender.sessions[3].labels.session: label name is reserved",
			"sender.sessions[3].labels.9bad:",
		}},
		{"too many labels", `
sender:
  enabled: true
  sessions:
    - {name: a, target: x, schedule: {frequency: 60s}, labels: {a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8, i: 9}}
`, []string{"sender.sessions[0].labels: 9 labels exceed the limit of 8"}},
		{"reflector", `
reflector:
  control: {listen: ["bogus"], acl: ["10.0.0.0/33"], pbkdf2_count: 100, verify_sender: maybe, test_port_range: "0-1"}
  light:
    listeners: [{listen: "0.0.0.0:862"}, {listen: "0.0.0.0:862"}, {listen: "host:1"}]
    acl: ["nope"]
    sequence: random
    dscp_policy: keep
    max_flows: 0
  ttl: 0
  max_packet_size: 10
`, []string{
			"reflector.control.listen[0]:", "reflector.control.acl[0]:", "reflector.control.pbkdf2_count:",
			"reflector.control.verify_sender:", "reflector.control.test_port_range:",
			"reflector.light.listeners[1]: 0.0.0.0:862 is listed twice", "reflector.light.listeners[2]:",
			"reflector.light.acl[0]:", "reflector.light.sequence:", "reflector.light.dscp_policy:",
			"reflector.light.max_flows:", "reflector.ttl:", "reflector.max_packet_size:",
		}},
		{"jsonl", `export: {jsonl: {enabled: true, path: "", records: [everything]}}`, []string{"export.jsonl.path:", "export.jsonl.records[0]:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			var verr *Error
			if !errors.As(err, &verr) {
				t.Fatalf("Parse error = %v, want *Error", err)
			}
			msg := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("missing %q in:\n%s", w, msg)
				}
			}
			if len(verr.Problems) != len(tc.want) {
				t.Errorf("%d problems, want %d:\n%s", len(verr.Problems), len(tc.want), msg)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"unknown key", "daemon:\n  colour: red\n", "line 2: field colour not found"},
		{"unknown session key", "sender:\n  sessions:\n    - name: a\n      targett: x\n", "field targett not found"},
		{"duration type", "sender:\n  defaults:\n    interval: 5\n", "cannot unmarshal"},
		{"bad dscp", "sender:\n  defaults:\n    dscp: af99\n", "line 3: dscp \"af99\""},
		{"bad size", "export:\n  jsonl:\n    rotate:\n      max_size: lots\n", "line 4: size \"lots\""},
		{"syntax", "daemon: [\n", "yaml:"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
}

func TestWarnings(t *testing.T) {
	c, err := Parse([]byte(`
sender:
  defaults: {padding: 1500}
  sessions: [{name: a, target: x, schedule: {frequency: 60s}}]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var got []string
	for _, w := range c.Warnings() {
		got = append(got, w.String())
	}
	joined := strings.Join(got, "\n")
	for _, w := range []string{"sender.defaults.padding:", "sender.sessions[0].padding:", "sender.sessions: 1 sessions are defined but sender.enabled is false"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning %q in:\n%s", w, joined)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte("daemon: {log: {level: debug}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(good)
	if err != nil || c.Daemon.Log.Level != "debug" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("version: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("Load(bad) = %v, want an error naming the file", err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load(missing) = %v, want os.ErrNotExist", err)
	}
}
