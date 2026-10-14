package config

import (
	"fmt"
	"maps"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Problem is one finding of Validate.
type Problem struct {
	// Path is the dotted path of the offending key, such as
	// "sender.sessions[2].interval". Empty for problems of the whole file.
	Path string
	// Message says what is wrong.
	Message string
}

// String formats p as "path: message".
func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// Error is a configuration that failed validation. It lists every problem.
type Error struct {
	Problems []Problem
}

// Error lists the problems, one per line.
func (e *Error) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = "  " + p.String()
	}
	noun := "problems"
	if len(e.Problems) == 1 {
		noun = "problem"
	}
	return fmt.Sprintf("invalid configuration (%d %s):\n%s", len(e.Problems), noun, strings.Join(lines, "\n"))
}

// Validation limits (SPEC §9.2).
const (
	maxCount          = 1_000_000
	maxPadding        = 9000
	unfragmentedLimit = 1472 // largest Sender payload in one IPv4 packet on a 1500 MTU
	maxLabels         = 8
	minWindowPackets  = 10
	maxUDPPayload     = 65507
	maxSocketPath     = 107 // sun_path holds 108 bytes, NUL included
	// maxTestDuration bounds the interval and the timeouts of a test, so
	// that one request cannot hold a socket or a run for days.
	maxTestDuration = time.Hour
	// maxScheduleDuration bounds the frequency, jitter and window of a session.
	maxScheduleDuration = 24 * time.Hour
	maxTxTimestampWait  = time.Second
	maxHostName         = 253
	maxLabelValue       = 256
	maxConcurrency      = 1024
	maxKeyID            = 80 // RFC 4656 §3.1
)

var (
	sessionNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	labelKeyRE    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	// reservedLabel are the label names the Prometheus exporter uses itself
	// on session metrics (export/prom's reserved).
	reservedLabel = []string{"session", "target", "mode", "direction", "stat", "kind", "result", "stage", "le"}
	jsonlRecords  = []string{"run_summary", "window_summary", "run_error", "packet"}
)

// checker collects problems and warnings.
type checker struct {
	problems, warnings []Problem
}

func (k *checker) errorf(path, format string, args ...any) {
	k.problems = append(k.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (k *checker) warnf(path, format string, args ...any) {
	k.warnings = append(k.warnings, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (k *checker) oneOf(path, value string, allowed ...string) {
	if !slices.Contains(allowed, value) {
		k.errorf(path, "%q is not one of %s", value, strings.Join(allowed, ", "))
	}
}

func (k *checker) between(path string, v, lo, hi int) {
	if v < lo || v > hi {
		k.errorf(path, "%d is out of range %d-%d", v, lo, hi)
	}
}

func (k *checker) positive(path string, d time.Duration) {
	if d <= 0 {
		k.errorf(path, "must be positive, got %v", d)
	}
}

func (k *checker) nonNegative(path string, d time.Duration) {
	if d < 0 {
		k.errorf(path, "must not be negative, got %v", d)
	}
}

func (k *checker) addrPorts(path string, list []string) {
	seen := map[string]bool{}
	for i, s := range list {
		p := fmt.Sprintf("%s[%d]", path, i)
		ap, err := ParseAddrPort(s)
		if err != nil {
			k.errorf(p, "%v", err)
			continue
		}
		if seen[ap.String()] {
			k.errorf(p, "%v is listed twice", ap)
		}
		seen[ap.String()] = true
	}
}

func (k *checker) prefixes(path string, list []string) {
	for i, s := range list {
		if _, err := ParsePrefix(s); err != nil {
			k.errorf(fmt.Sprintf("%s[%d]", path, i), "%v", err)
		}
	}
}

func (k *checker) portRange(path, s string) {
	if _, err := ParsePortRange(s); err != nil {
		k.errorf(path, "%v", err)
	}
}

// Validate checks c, records its warnings (see Warnings) and returns an
// *Error when anything is wrong.
func (c *Config) Validate() error {
	var k checker
	if c.Version != 1 {
		k.errorf("version", "%d is not supported (want 1)", c.Version)
	}
	if !c.Sender.Enabled && !c.Reflector.Enabled {
		k.errorf("", "neither sender.enabled nor reflector.enabled is true")
	}
	k.daemon(&c.Daemon)
	k.sender(&c.Sender)
	if c.Reflector.Enabled {
		k.reflector(&c.Reflector)
	}
	k.store(&c.Store)
	if j := c.Export.JSONL; j.Enabled {
		if j.Path == "" {
			k.errorf("export.jsonl.path", "is required when export.jsonl.enabled is true")
		}
		for i, r := range j.Records {
			k.oneOf(fmt.Sprintf("export.jsonl.records[%d]", i), r, jsonlRecords...)
		}
		k.between("export.jsonl.rotate.max_files", j.Rotate.MaxFiles, 0, 1_000_000)
	}
	c.warnings = k.warnings
	return k.err()
}

func (k *checker) daemon(d *Daemon) {
	if !filepath.IsAbs(d.API.Socket) {
		k.errorf("daemon.api.socket", "%q is not an absolute path", d.API.Socket)
	} else if len(d.API.Socket) > maxSocketPath {
		k.errorf("daemon.api.socket", "path is %d bytes long; Unix domain sockets allow at most %d", len(d.API.Socket), maxSocketPath)
	}
	if _, err := d.API.FileMode(); err != nil {
		k.errorf("daemon.api.socket_mode", "%v", err)
	}
	k.apiTCP(&d.API.TCP)
	if d.Metrics.Listen != "" {
		if _, port, err := net.SplitHostPort(d.Metrics.Listen); err != nil {
			k.errorf("daemon.metrics.listen", "%q is not host:port", d.Metrics.Listen)
		} else if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
			k.errorf("daemon.metrics.listen", "port %q is not 1-65535", port)
		}
	}
	for i, b := range d.Metrics.RTTBuckets {
		if b <= 0 || (i > 0 && b <= d.Metrics.RTTBuckets[i-1]) {
			k.errorf(fmt.Sprintf("daemon.metrics.rtt_buckets[%d]", i), "buckets must be positive and increasing, got %v", b)
		}
	}
	k.oneOf("daemon.log.level", d.Log.Level, "debug", "info", "warn", "error")
	k.oneOf("daemon.log.format", d.Log.Format, "json", "text")
	k.oneOf("daemon.timestamping", d.Timestamping, "auto", "hardware", "kernel", "userspace")
	if n := d.HardwareInterface; n != "" {
		// IFNAMSIZ is 16 with the NUL; a name has no slash or white space.
		if len(n) > 15 || strings.ContainsAny(n, "/ \t\n") {
			k.errorf("daemon.hardware_interface", "%q is not an interface name", n)
		}
		if d.Timestamping != "hardware" {
			k.warnf("daemon.hardware_interface", "is ignored unless daemon.timestamping is hardware")
		}
	}
	k.oneOf("daemon.clock.sync", d.Clock.Sync, "auto", "synced", "unsynced")
	k.nonNegative("daemon.clock.max_error", d.Clock.MaxError)
	k.nonNegative("daemon.clock.userspace_error", d.Clock.UserspaceError)
	k.nonNegative("daemon.clock.kernel_error", d.Clock.KernelError)
	k.nonNegative("daemon.clock.hardware_error", d.Clock.HardwareError)
	k.between("daemon.limits.max_samples_per_run", d.Limits.MaxSamplesPerRun, 1, 10_000_000)
	k.between("daemon.limits.result_history", d.Limits.ResultHistory, 1, 1_000_000)
	k.between("daemon.limits.max_concurrent_tests", d.Limits.MaxConcurrentTests, 1, maxConcurrency)
	k.between("daemon.limits.max_watchers", d.Limits.MaxWatchers, 1, maxConcurrency)
}

// apiTCP checks the API over TCP, which is never served without mutual TLS
// (ADR 0010).
func (k *checker) apiTCP(t *APITCP) {
	const path = "daemon.api.tcp"
	if t.Listen == "" {
		if t.CA != "" || t.Cert != "" || t.Key != "" || len(t.AllowedNames) > 0 {
			k.warnf(path, "TLS settings are ignored while daemon.api.tcp.listen is empty")
		}
		return
	}
	if _, err := ParseAddrPort(t.Listen); err != nil {
		k.errorf(path+".listen", "%v", err)
	}
	for _, f := range []struct{ key, value string }{{"ca", t.CA}, {"cert", t.Cert}, {"key", t.Key}} {
		if f.value == "" {
			k.errorf(path+"."+f.key, "is required: the API is served over TCP with mutual TLS only")
		} else if !filepath.IsAbs(f.value) {
			k.errorf(path+"."+f.key, "%q is not an absolute path", f.value)
		}
	}
	for i, n := range t.AllowedNames {
		if strings.TrimSpace(n) == "" {
			k.errorf(fmt.Sprintf("%s.allowed_names[%d]", path, i), "is empty")
		}
	}
	if len(t.AllowedNames) == 0 {
		k.warnf(path+".allowed_names", "is empty: every client certificate the CA signs may manage gotwampd")
	}
}

// security checks the security mode and key of a test at path.
func (k *checker) security(path string, p *TestParams, keysFile string) {
	if p.Security == "" || p.Security == "unauthenticated" {
		return
	}
	if p.KeyID == "" {
		k.errorf(path+".key_id", "is required with security %s", p.Security)
	}
	if keysFile == "" {
		k.errorf("sender.keys_file", "is required with security %s at %s", p.Security, path)
	}
}

func (k *checker) sender(s *Sender) {
	k.params("sender.defaults", &s.Defaults, true)
	k.security("sender.defaults", &s.Defaults, s.KeysFile)
	if s.Defaults.Mode == "light" && s.Defaults.Security != "unauthenticated" {
		k.errorf("sender.defaults.security", "TWAMP Light is unauthenticated only")
	}
	if len(s.Sessions) > 0 && !s.Enabled {
		k.warnf("sender.sessions", "%d sessions are defined but sender.enabled is false", len(s.Sessions))
	}
	names := map[string]int{}
	for i := range s.Sessions {
		ss := &s.Sessions[i]
		path := fmt.Sprintf("sender.sessions[%d]", i)
		if j, dup := names[ss.Name]; dup && sessionNameRE.MatchString(ss.Name) {
			k.errorf(path+".name", "%q is also the name of sender.sessions[%d]", ss.Name, j)
		}
		names[ss.Name] = i
		k.session(path, ss)
		explicit := ss.Security != s.Defaults.Security || ss.KeyID != s.Defaults.KeyID
		switch {
		case ss.Mode == "light" && explicit && ss.Security != "unauthenticated":
			// A Light session ignores the mode sender.defaults gives it, but
			// one it asks for itself is a mistake (RFC 5357 Appendix I
			// leaves the keys of Light undefined).
			k.errorf(path+".security", "TWAMP Light is unauthenticated only")
		case explicit && ss.Mode != "light":
			k.security(path, &ss.TestParams, s.KeysFile)
		}
	}
}

// session checks one monitoring session at path.
func (k *checker) session(path string, ss *Session) {
	if !sessionNameRE.MatchString(ss.Name) {
		k.errorf(path+".name", "%q does not match %s", ss.Name, sessionNameRE)
	}
	k.endpoints(path, ss.Target, ss.IPV, ss.Source)
	k.schedule(path, ss)
	k.params(path, &ss.TestParams, ss.Schedule.Type != "continuous")
	k.labels(path+".labels", ss.Labels)
}

// endpoints checks the target and the address selection of a test.
func (k *checker) endpoints(path, target, ipv, source string) {
	if target == "" {
		k.errorf(path+".target", "is required")
	} else if !validTarget(target) {
		k.errorf(path+".target", "%q is neither an IP address nor a host name", target)
	}
	if ipv != "" {
		k.oneOf(path+".ipv", ipv, "auto", "4", "6")
	}
	if source != "" && net.ParseIP(source) == nil {
		k.errorf(path+".source", "%q is not an IP address", source)
	}
}

// ValidName reports whether name is a valid monitoring session name:
// [a-z0-9][a-z0-9_-]{0,62}.
func ValidName(name string) bool { return sessionNameRE.MatchString(name) }

// ValidateSession checks one monitoring session, such as one created
// through the API, as Validate checks each of sender.sessions[]. Problems
// are reported as an *Error with paths relative to the session.
func ValidateSession(s *Session) error {
	var k checker
	k.session("session", s)
	return k.err()
}

// ValidateTest checks the parameters of an ad-hoc test: target, address
// selection and test parameters, with a count of 1 to maxCount.
func ValidateTest(target, ipv, source string, p *TestParams) error {
	var k checker
	k.endpoints("test", target, ipv, source)
	k.params("test", p, true)
	return k.err()
}

func (k *checker) err() error {
	if len(k.problems) > 0 {
		return &Error{Problems: k.problems}
	}
	return nil
}

func (k *checker) schedule(path string, s *Session) {
	sc := s.Schedule
	switch sc.Type {
	case "periodic", "":
		k.positive(path+".schedule.frequency", sc.Frequency)
		k.atMost(path+".schedule.frequency", sc.Frequency, maxScheduleDuration)
		k.nonNegative(path+".schedule.jitter", sc.Jitter)
		k.atMost(path+".schedule.jitter", sc.Jitter, maxScheduleDuration)
		need := runLength(s.Count, s.Interval, s.Timeout)
		if sc.Frequency > 0 && sc.Frequency < need {
			k.errorf(path+".schedule.frequency", "%v is shorter than count × interval + timeout + 1s = %v", sc.Frequency, need)
		}
	case "continuous":
		k.positive(path+".schedule.window", sc.Window)
		k.atMost(path+".schedule.window", sc.Window, maxScheduleDuration)
		if sc.Window > 0 && s.Interval > 0 && sc.Window/s.Interval < minWindowPackets {
			k.errorf(path+".schedule.window", "%v holds fewer than %d intervals of %v", sc.Window, minWindowPackets, s.Interval)
		}
	default:
		k.oneOf(path+".schedule.type", sc.Type, "periodic", "continuous")
	}
}

// params checks test parameters. checkCount is false for continuous
// sessions, whose count follows from the window.
func (k *checker) params(path string, p *TestParams, checkCount bool) {
	k.oneOf(path+".mode", p.Mode, "full", "light")
	k.between(path+".control_port", p.ControlPort, 1, 65535)
	k.between(path+".receiver_port", p.ReceiverPort, 0, 65535)
	k.between(path+".sender_port", p.SenderPort, 0, 65535)
	k.portRange(path+".sender_port_range", p.SenderPortRange)
	if checkCount {
		k.between(path+".count", p.Count, 1, maxCount)
	}
	if p.Interval < time.Millisecond {
		k.errorf(path+".interval", "%v is shorter than 1ms", p.Interval)
	}
	k.atMost(path+".interval", p.Interval, maxTestDuration)
	k.positive(path+".timeout", p.Timeout)
	k.atMost(path+".timeout", p.Timeout, maxTestDuration)
	k.between(path+".padding", p.Padding, 0, maxPadding)
	if p.Padding > unfragmentedLimit-14 {
		k.warnf(path+".padding", "%d makes the packet larger than %d octets, which fragments on a 1500-octet IPv4 path", p.Padding, unfragmentedLimit)
	}
	k.oneOf(path+".padding_fill", p.PaddingFill, "auto", "random", "zero")
	k.between(path+".ttl", p.TTL, 1, 255)
	k.positive(path+".control_timeout", p.ControlTimeout)
	k.atMost(path+".control_timeout", p.ControlTimeout, maxTestDuration)
	k.nonNegative(path+".end_delay", p.EndDelay)
	k.atMost(path+".end_delay", p.EndDelay, maxTestDuration)
	k.oneOf(path+".start_time", p.StartTime, "zero", "now")
	k.nonNegative(path+".tx_timestamp_wait", p.TxTimestampWait)
	k.atMost(path+".tx_timestamp_wait", p.TxTimestampWait, maxTxTimestampWait)
	k.oneOf(path+".reflector_sequence", p.ReflectorSequence, "stateful", "copy_sender")
	k.oneOf(path+".security", p.Security, "unauthenticated", "authenticated", "encrypted", "mixed")
	if len(p.KeyID) > maxKeyID || !utf8.ValidString(p.KeyID) {
		k.errorf(path+".key_id", "is not UTF-8 of at most %d octets", maxKeyID)
	}
	if p.DSCPECNMonitoring && p.Mode == "full" {
		// Full would have to negotiate it in the Modes field (RFC 7750 §3.1).
		k.errorf(path+".dscp_ecn_monitoring", "is supported in light mode only")
	}
	k.list(path+".extensions", p.Extensions, extensions)
	k.between(path+".octets_to_reflect", p.OctetsToReflect, 0, 65535)
	k.between(path+".padding_to_reflect", p.PaddingToReflect, 0, 65535)
	k.between(path+".ssid", p.SSID, 0, 65535)
	switch {
	case p.STAMP && p.DSCPECNMonitoring:
		k.errorf(path+".stamp", "excludes dscp_ecn_monitoring: STAMP keeps offset 41 MBZ (RFC 8762 §4.3.1)")
	case p.STAMP && p.Padding > 0 && p.Padding < 4:
		k.errorf(path+".padding", "%d is shorter than the 4-octet STAMP Extra Padding TLV header", p.Padding)
	case p.SSID != 0 && !p.STAMP:
		k.errorf(path+".ssid", "needs stamp")
	}
	for _, key := range p.otherModeKeys() {
		k.errorf(path+"."+key, "does not apply to %s mode", p.Mode)
	}
}

// otherModeKeys lists the keys p sets that apply to the other mode only:
// the Full extensions and security in a Light test, STAMP in a Full one.
func (p *TestParams) otherModeKeys() []string {
	var keys []string
	switch p.Mode {
	case "light":
		if len(p.Extensions) > 0 {
			keys = append(keys, "extensions")
		}
		if p.OctetsToReflect != 0 {
			keys = append(keys, "octets_to_reflect")
		}
		if p.PaddingToReflect != 0 {
			keys = append(keys, "padding_to_reflect")
		}
	case "full":
		if p.STAMP {
			keys = append(keys, "stamp")
		}
	}
	return keys
}

// list checks that every value of a list is one of allowed, once.
func (k *checker) list(path string, values, allowed []string) {
	seen := map[string]bool{}
	for i, v := range values {
		p := fmt.Sprintf("%s[%d]", path, i)
		k.oneOf(p, v, allowed...)
		if seen[v] {
			k.errorf(p, "%q is listed twice", v)
		}
		seen[v] = true
	}
}

// runLength is count × interval + timeout + 1s, saturating instead of
// overflowing for out-of-range inputs.
func runLength(count int, interval, timeout time.Duration) time.Duration {
	const limit = time.Duration(math.MaxInt64)
	if count < 0 || interval < 0 || timeout < 0 {
		return 0
	}
	if interval > 0 && time.Duration(count) > (limit-timeout-time.Second)/interval {
		return limit
	}
	return time.Duration(count)*interval + timeout + time.Second
}

// hostChar reports whether c may appear in a host name label.
func hostChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func (k *checker) atMost(path string, d, limit time.Duration) {
	if d > limit {
		k.errorf(path, "%v exceeds %v", d, limit)
	}
}

// validTarget accepts an IP address (with a zone for link-local ones) or a
// host name of letters, digits, '-' and '_' in dot-separated labels of at
// most 63 octets, 253 octets in all, with an optional trailing dot.
func validTarget(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if len(s) > maxHostName {
		return false
	}
	labels := strings.Split(strings.TrimSuffix(s, "."), ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !hostChar(c) {
				return false
			}
		}
	}
	return true
}

func (k *checker) labels(path string, labels map[string]string) {
	if len(labels) > maxLabels {
		k.errorf(path, "%d labels exceed the limit of %d", len(labels), maxLabels)
	}
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		switch {
		case !labelKeyRE.MatchString(key):
			k.errorf(path+"."+key, "label name does not match %s", labelKeyRE)
		case slices.Contains(reservedLabel, key):
			k.errorf(path+"."+key, "label name is reserved")
		case len(labels[key]) > maxLabelValue || !utf8.ValidString(labels[key]):
			k.errorf(path+"."+key, "label value must be valid UTF-8 of at most %d octets", maxLabelValue)
		}
	}
}

func (k *checker) reflector(r *Reflector) {
	c := r.Control
	k.addrPorts("reflector.control.listen", c.Listen)
	k.prefixes("reflector.control.acl", c.ACL)
	k.between("reflector.control.max_connections", c.MaxConnections, 1, 1_000_000)
	k.between("reflector.control.max_sessions_per_connection", c.MaxSessionsPerConnection, 1, 1_000_000)
	k.between("reflector.control.max_sessions", c.MaxSessions, 1, 1_000_000)
	k.positive("reflector.control.servwait", c.ServWait)
	k.positive("reflector.control.refwait", c.RefWait)
	k.positive("reflector.control.partial_message_timeout", c.PartialMessageTimeout)
	k.portRange("reflector.control.test_port_range", c.TestPortRange)
	k.oneOf("reflector.control.verify_sender", c.VerifySender, "address", "address_port", "none")
	// The control server takes 0 for its default, 9000: 0 cannot mean "no
	// padding" there, so it is refused rather than silently replaced.
	k.between("reflector.control.max_padding", c.MaxPadding, 1, maxUDPPayload)
	// RFC 4656 §3.1: Count is at least 1024; clients refuse more than 32768.
	// It must be a power of 2, which the control server checks at startup.
	k.between("reflector.control.pbkdf2_count", c.PBKDF2Count, 1024, 32768)
	k.controlModes(c)
	if n := c.PBKDF2Count; n >= 1024 && n <= 32768 && n&(n-1) != 0 {
		k.errorf("reflector.control.pbkdf2_count", "%d is not a power of 2", n)
	}

	k.list("reflector.control.extensions", c.Extensions, extensions)

	l := r.Light
	if l.STAMP && l.DSCPECNMonitoring {
		k.errorf("reflector.light.stamp", "excludes dscp_ecn_monitoring: STAMP keeps offset 41 MBZ (RFC 8762 §4.3.1)")
	}
	listen := make([]string, len(l.Listeners))
	for i, ln := range l.Listeners {
		listen[i] = ln.Listen
	}
	k.addrPorts("reflector.light.listeners", listen)
	k.prefixes("reflector.light.acl", l.ACL)
	k.oneOf("reflector.light.sequence", l.Sequence, "per_flow", "copy_sender")
	k.positive("reflector.light.flow_timeout", l.FlowTimeout)
	k.between("reflector.light.max_flows", l.MaxFlows, 1, 100_000_000)
	k.between("reflector.light.rate_limit_pps", l.RateLimitPPS, 0, 100_000_000)
	k.oneOf("reflector.light.dscp_policy", l.DSCPPolicy, "copy", "fixed")

	k.between("reflector.ttl", r.TTL, 1, 255)
	k.between("reflector.max_packet_size", r.MaxPacketSize, 14, maxUDPPayload)
	if len(c.Listen) == 0 && len(l.Listeners) == 0 {
		k.warnf("reflector", "is enabled but lists no address in control.listen or light.listeners: nothing is reflected")
	}
}

// store checks the result store.
func (k *checker) store(s *Store) {
	k.oneOf("store.backend", s.Backend, "memory", "sqlite")
	if s.Backend != "sqlite" {
		return
	}
	q := s.SQLite
	if !filepath.IsAbs(q.Path) {
		k.errorf("store.sqlite.path", "%q is not an absolute path", q.Path)
	}
	k.nonNegative("store.sqlite.retention", q.Retention)
	if q.MaxSize < 0 {
		k.errorf("store.sqlite.max_size", "must not be negative")
	}
	k.oneOf("store.sqlite.on_corrupt", q.OnCorrupt, "reset", "fail")
}

// controlModes checks the security modes the control server offers: the
// modes other than unauthenticated need the keys file.
func (k *checker) controlModes(c ControlServer) {
	seen := map[string]bool{}
	for i, m := range c.Modes {
		p := fmt.Sprintf("reflector.control.modes[%d]", i)
		k.oneOf(p, m, securityModes...)
		if seen[m] {
			k.errorf(p, "%q is listed twice", m)
		}
		seen[m] = true
		if m != "unauthenticated" && slices.Contains(securityModes, m) && c.KeysFile == "" {
			k.errorf(p, "%s needs reflector.control.keys_file", m)
		}
	}
	if c.KeysFile != "" && len(c.Modes) > 0 && !slices.ContainsFunc(c.Modes, func(m string) bool { return m != "unauthenticated" }) {
		k.warnf("reflector.control.keys_file", "is unused: reflector.control.modes offers unauthenticated only")
	}
}

// checkSecretFiles refuses private keys and shared secrets that others than
// their owner may read (mode beyond 0600) or that do not exist.
//
//declscope:package // Load checks the files of the configuration it read
func (c *Config) checkSecretFiles() error {
	var k checker
	for _, f := range []struct{ key, path string }{
		{"daemon.api.tcp.key", c.Daemon.API.TCP.Key},
		{"sender.keys_file", c.Sender.KeysFile},
		{"reflector.control.keys_file", c.Reflector.Control.KeysFile},
	} {
		if f.path == "" {
			continue
		}
		fi, err := os.Stat(f.path)
		switch {
		case err != nil:
			k.errorf(f.key, "%v", err)
		case fi.Mode().Perm()&0o077 != 0:
			k.errorf(f.key, "%s has mode %v; a secret must be readable by its owner only (0600 or less)", f.path, fi.Mode().Perm())
		}
	}
	return k.err()
}
