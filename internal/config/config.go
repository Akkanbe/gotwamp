// Package config reads, defaults and validates the gotwampd configuration
// file (SPEC §9.2, appendix A).
//
// Parse starts from Default, overlays the YAML document and then gives every
// monitoring session a copy of sender.defaults overlaid with the session's own
// keys. Unknown keys are errors. Validation reports every problem it finds,
// each with the path of the offending key.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where gotwampd looks for its configuration file.
const DefaultPath = "/etc/gotwamp/gotwampd.yaml"

// Config is the whole configuration file.
type Config struct {
	// Version is the schema version; only 1 exists.
	Version   int       `yaml:"version"`
	Daemon    Daemon    `yaml:"daemon"`
	Sender    Sender    `yaml:"sender"`
	Reflector Reflector `yaml:"reflector"`
	Export    Export    `yaml:"export"`
	Store     Store     `yaml:"store"`

	// warnings are problems that do not stop the daemon.
	//
	//declscope:package // Validate (validate.go) records them
	warnings []Problem
}

// Daemon holds the process-wide settings.
type Daemon struct {
	API          API     `yaml:"api"`
	Metrics      Metrics `yaml:"metrics"`
	Log          Log     `yaml:"log"`
	Timestamping string  `yaml:"timestamping"` // auto | hardware | kernel | userspace
	// HardwareInterface names the network interface whose card timestamps
	// the sockets bound to the wildcard address with timestamping
	// hardware. Empty leaves those sockets on kernel timestamps, except a
	// Light sender's, which takes the interface of the route to its target.
	HardwareInterface string   `yaml:"hardware_interface"`
	Clock             Clock    `yaml:"clock"`
	Realtime          Realtime `yaml:"realtime"`
	Limits            Limits   `yaml:"limits"`
}

// DefaultSocketGroup is the default daemon.api.socket_group, the group the
// packages create. A daemon started where it is missing, or not one of its
// groups, keeps the process's group.
const DefaultSocketGroup = "gotwamp"

// API configures the gRPC Unix domain socket.
type API struct {
	Socket      string `yaml:"socket"`
	SocketMode  string `yaml:"socket_mode"`  // octal, such as "0660"
	SocketGroup string `yaml:"socket_group"` // empty keeps the process's group
	// TCP also serves the API over TCP with mutual TLS, for remote
	// management (ADR 0010). The Unix domain socket stays.
	TCP APITCP `yaml:"tcp"`
}

// APITCP configures the API over TCP. It is off while Listen is empty, and
// never served without mutual TLS.
type APITCP struct {
	// Listen is the IP address and port to listen on.
	Listen string `yaml:"listen"`
	// CA is the PEM bundle of the CAs that sign client certificates.
	CA string `yaml:"ca"`
	// Cert and Key are the PEM certificate chain and private key of
	// gotwampd. Key must be readable by its owner only.
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
	// AllowedNames are the client identities allowed in: the Common Name
	// or a DNS, IP, URI or email Subject Alternative Name of the client
	// certificate. Empty allows every certificate CA signed.
	AllowedNames []string `yaml:"allowed_names"`
}

// Metrics configures the Prometheus endpoint.
type Metrics struct {
	Listen     string          `yaml:"listen"` // empty disables it
	RTTBuckets []time.Duration `yaml:"rtt_buckets"`
}

// Log configures log/slog output.
type Log struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // json | text
}

// Clock overrides the clock state read from the kernel.
type Clock struct {
	Sync           string        `yaml:"sync"`      // auto | synced | unsynced
	MaxError       time.Duration `yaml:"max_error"` // 0 uses adjtimex's maxerror
	UserspaceError time.Duration `yaml:"userspace_error"`
	// KernelError is the method error of kernel timestamps (SPEC §8.4).
	KernelError time.Duration `yaml:"kernel_error"`
	// HardwareError is the method error of a network card's timestamps.
	HardwareError time.Duration `yaml:"hardware_error"`
}

// Realtime holds scheduling knobs of the hot paths.
type Realtime struct {
	LockOSThread bool `yaml:"lock_os_thread"`
}

// Limits bound the memory of the result store.
type Limits struct {
	MaxSamplesPerRun int `yaml:"max_samples_per_run"`
	ResultHistory    int `yaml:"result_history"`
	// MaxConcurrentTests caps the ad-hoc tests (RunTest) running at once.
	MaxConcurrentTests int `yaml:"max_concurrent_tests"`
	// MaxWatchers caps the WatchResults subscriptions open at once.
	MaxWatchers int `yaml:"max_watchers"`
}

// Sender configures the sender role.
type Sender struct {
	Enabled bool `yaml:"enabled"`
	// KeysFile holds the shared secrets of the authenticated, encrypted
	// and mixed modes by KeyID, in the format of perfSONAR's pfs files.
	KeysFile string     `yaml:"keys_file"`
	Defaults TestParams `yaml:"defaults"`
	// Sessions inherit every key they leave out from Defaults.
	Sessions []Session `yaml:"sessions"`
}

// TestParams are the per-session test parameters; sender.defaults supplies
// the values a session leaves out.
type TestParams struct {
	Mode                   string        `yaml:"mode"` // full | light
	ControlPort            int           `yaml:"control_port"`
	ReceiverPort           int           `yaml:"receiver_port"`
	SenderPort             int           `yaml:"sender_port"`
	SenderPortRange        string        `yaml:"sender_port_range"` // "first-last"
	Count                  int           `yaml:"count"`
	Interval               time.Duration `yaml:"interval"`
	Timeout                time.Duration `yaml:"timeout"`
	Padding                int           `yaml:"padding"`
	PaddingFill            string        `yaml:"padding_fill"` // auto | random | zero
	DSCP                   DSCP          `yaml:"dscp"`
	TTL                    int           `yaml:"ttl"`
	ControlTimeout         time.Duration `yaml:"control_timeout"`
	ControlDSCP            DSCP          `yaml:"control_dscp"`
	EndDelay               time.Duration `yaml:"end_delay"`
	ZeroAddresses          bool          `yaml:"zero_addresses"`
	StartTime              string        `yaml:"start_time"` // zero | now
	AcceptAlternatePort    bool          `yaml:"accept_alternate_port"`
	TolerateZeroMultiplier bool          `yaml:"tolerate_zero_multiplier"`
	TxTimestampWait        time.Duration `yaml:"tx_timestamp_wait"`
	// ReflectorSequence says how the reflector numbers its replies: with
	// its own counter (stateful), or by copying the Sender Sequence Number
	// (copy_sender, RFC 5357 Appendix I), which leaves the loss split and
	// the reverse reordering undefined (SPEC §7.2).
	ReflectorSequence string `yaml:"reflector_sequence"` // stateful | copy_sender
	// DSCPECNMonitoring reads the DSCP and ECN the reflector received from
	// the RFC 7750 field of its replies; the reflector must write it.
	DSCPECNMonitoring bool `yaml:"dscp_ecn_monitoring"`
	// Security is the TWAMP-Control mode to ask for; the modes other than
	// unauthenticated need KeyID and sender.keys_file. TWAMP Light is
	// unauthenticated only: a Light session ignores the mode it inherits
	// from sender.defaults.
	Security string `yaml:"security"` // unauthenticated | authenticated | encrypted | mixed
	// KeyID names the shared secret in sender.keys_file (RFC 4656: up to 80
	// octets of UTF-8).
	KeyID string `yaml:"key_id"`
	// Extensions are the TWAMP extensions a Full test uses where the server
	// offers them: individual_session_control (RFC 5938), reflect_octets
	// and symmetrical_size (RFC 6038).
	Extensions []string `yaml:"extensions"`
	// OctetsToReflect and PaddingToReflect are the Reflect Octets fields of
	// a Full test's request (RFC 6038 §4.2): two octets the server echoes
	// in Accept-Session, and the length of padding the reflector returns
	// unchanged. Either non-zero asks for reflect_octets.
	OctetsToReflect  int `yaml:"octets_to_reflect"`
	PaddingToReflect int `yaml:"padding_to_reflect"`
	// STAMP sends STAMP Session-Sender packets (RFC 8762) in a Light test,
	// with the Session-Sender Identifier SSID (RFC 8972; 0 sends none).
	STAMP bool `yaml:"stamp"`
	SSID  int  `yaml:"ssid"`
}

// Session is a monitoring session (sender.sessions[]).
type Session struct {
	Name       string            `yaml:"name"`
	Target     string            `yaml:"target"`
	IPV        string            `yaml:"ipv"` // auto | 4 | 6
	Source     string            `yaml:"source"`
	Schedule   Schedule          `yaml:"schedule"`
	Labels     map[string]string `yaml:"labels"`
	TestParams `yaml:",inline"`
}

// Schedule says when a monitoring session runs.
type Schedule struct {
	Type      string        `yaml:"type"` // periodic | continuous
	Frequency time.Duration `yaml:"frequency"`
	Jitter    time.Duration `yaml:"jitter"`
	Window    time.Duration `yaml:"window"`
}

// Reflector configures the reflector role.
type Reflector struct {
	Enabled       bool           `yaml:"enabled"`
	Control       ControlServer  `yaml:"control"`
	Light         LightReflector `yaml:"light"`
	TTL           int            `yaml:"ttl"`
	MaxPacketSize int            `yaml:"max_packet_size"`
}

// ControlServer configures the TWAMP-Control server (Full mode).
type ControlServer struct {
	Listen                   []string      `yaml:"listen"`
	ACL                      []string      `yaml:"acl"`
	MaxConnections           int           `yaml:"max_connections"`
	MaxSessionsPerConnection int           `yaml:"max_sessions_per_connection"`
	MaxSessions              int           `yaml:"max_sessions"`
	ServWait                 time.Duration `yaml:"servwait"`
	RefWait                  time.Duration `yaml:"refwait"`
	PartialMessageTimeout    time.Duration `yaml:"partial_message_timeout"`
	TestPortRange            string        `yaml:"test_port_range"`
	HonorRequestedPort       bool          `yaml:"honor_requested_port"`
	VerifySender             string        `yaml:"verify_sender"` // address | address_port | none
	MaxPadding               int           `yaml:"max_padding"`
	PBKDF2Count              int           `yaml:"pbkdf2_count"`
	// KeysFile holds the shared secrets clients authenticate with, by
	// KeyID, in the format of perfSONAR's pfs files.
	KeysFile string `yaml:"keys_file"`
	// Modes are the security modes offered: unauthenticated, authenticated,
	// encrypted and mixed. Empty offers unauthenticated alone, or all four
	// when KeysFile is set (see OfferedModes).
	Modes []string `yaml:"modes"`
	// Extensions are the TWAMP extensions offered: individual_session_control
	// (RFC 5938), reflect_octets and symmetrical_size (RFC 6038). The client
	// picks among them, so offering costs nothing; all three by default.
	Extensions []string `yaml:"extensions"`
}

// LightReflector configures the TWAMP Light reflector.
type LightReflector struct {
	Listeners    []LightListener `yaml:"listeners"`
	ACL          []string        `yaml:"acl"`
	Sequence     string          `yaml:"sequence"` // per_flow | copy_sender
	FlowTimeout  time.Duration   `yaml:"flow_timeout"`
	MaxFlows     int             `yaml:"max_flows"`
	RateLimitPPS int             `yaml:"rate_limit_pps"`
	DSCPPolicy   string          `yaml:"dscp_policy"` // copy | fixed
	DSCPFixed    DSCP            `yaml:"dscp_fixed"`
	// DSCPECNMonitoring answers in the RFC 7750 layout, reporting the DSCP
	// and ECN each test packet arrived with; Senders must expect it.
	DSCPECNMonitoring bool `yaml:"dscp_ecn_monitoring"`
	// STAMP answers in the STAMP layout (RFC 8762, RFC 8972 SSID); TWAMP
	// Light senders are answered as before, their padding untouched.
	STAMP bool `yaml:"stamp"`
}

// LightListener is one UDP address the Light reflector listens on.
type LightListener struct {
	Listen string `yaml:"listen"`
}

// Store configures the result store: in memory, or kept in a SQLite
// database across restarts.
type Store struct {
	Backend string      `yaml:"backend"` // memory | sqlite
	SQLite  SQLiteStore `yaml:"sqlite"`
}

// SQLiteStore configures the SQLite result store.
type SQLiteStore struct {
	// Path is the database file.
	Path string `yaml:"path"`
	// Retention is how long results are kept; zero keeps them for ever.
	Retention time.Duration `yaml:"retention"`
	// MaxSize is the size of the database above which gotwampd warns; zero
	// never warns.
	MaxSize Size `yaml:"max_size"`
	// OnCorrupt is what a corrupt database leads to: reset moves it aside
	// and starts an empty one, fail stops gotwampd.
	OnCorrupt string `yaml:"on_corrupt"` // reset | fail
}

// Export configures the result outputs.
type Export struct {
	JSONL JSONL `yaml:"jsonl"`
}

// JSONL configures the JSON Lines writer.
type JSONL struct {
	Enabled bool     `yaml:"enabled"`
	Path    string   `yaml:"path"`
	Records []string `yaml:"records"`
	Rotate  Rotate   `yaml:"rotate"`
}

// Rotate configures the rotation of the JSON Lines file.
type Rotate struct {
	MaxSize  Size `yaml:"max_size"`
	MaxFiles int  `yaml:"max_files"`
	// Daily also starts a new file at the first record of each day.
	Daily bool `yaml:"daily"`
}

// Default returns the configuration used when no file exists: the reflector
// role alone, with every value at its default (SPEC appendix A).
func Default() *Config {
	return &Config{
		Version: 1,
		Daemon: Daemon{
			API: API{Socket: "/run/gotwamp/gotwampd.sock", SocketMode: "0660", SocketGroup: DefaultSocketGroup},
			Metrics: Metrics{
				Listen: "127.0.0.1:9862",
				RTTBuckets: []time.Duration{
					50 * time.Microsecond, 100 * time.Microsecond, 200 * time.Microsecond, 500 * time.Microsecond,
					time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
					20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
					500 * time.Millisecond, time.Second,
				},
			},
			Log:          Log{Level: "info", Format: "json"},
			Timestamping: "auto",
			Clock: Clock{
				Sync: "auto", UserspaceError: 50 * time.Microsecond, KernelError: 5 * time.Microsecond,
				HardwareError: time.Microsecond,
			},
			Limits: Limits{MaxSamplesPerRun: 100_000, ResultHistory: 1440, MaxConcurrentTests: 16, MaxWatchers: 32},
		},
		Sender: Sender{
			Defaults: TestParams{
				Mode:                   "full",
				ControlPort:            862,
				SenderPortRange:        "49152-57343",
				Count:                  100,
				Interval:               100 * time.Millisecond,
				Timeout:                2 * time.Second,
				Padding:                27,
				PaddingFill:            "auto",
				TTL:                    255,
				ControlTimeout:         15 * time.Second,
				StartTime:              "zero",
				AcceptAlternatePort:    true,
				TolerateZeroMultiplier: true,
				TxTimestampWait:        time.Millisecond,
				ReflectorSequence:      "stateful",
				Security:               "unauthenticated",
			},
		},
		Reflector: Reflector{
			Enabled: true,
			Control: ControlServer{
				Listen:                   []string{"0.0.0.0:862", "[::]:862"},
				MaxConnections:           64,
				MaxSessionsPerConnection: 16,
				MaxSessions:              1024,
				ServWait:                 900 * time.Second,
				RefWait:                  900 * time.Second,
				PartialMessageTimeout:    60 * time.Second,
				TestPortRange:            "20000-29999",
				HonorRequestedPort:       true,
				VerifySender:             "address",
				MaxPadding:               9000,
				PBKDF2Count:              2048,
				Extensions:               slices.Clone(extensions),
			},
			Light: LightReflector{
				Listeners:   []LightListener{{Listen: "0.0.0.0:862"}, {Listen: "[::]:862"}},
				Sequence:    "per_flow",
				FlowTimeout: 900 * time.Second,
				MaxFlows:    65536,
				DSCPPolicy:  "copy",
			},
			TTL:           255,
			MaxPacketSize: 9000,
		},
		Store: Store{
			Backend: "memory",
			SQLite: SQLiteStore{
				Path: "/var/lib/gotwamp/results.db", Retention: 30 * 24 * time.Hour, OnCorrupt: "reset",
			},
		},
		Export: Export{
			JSONL: JSONL{
				Path:    "/var/lib/gotwamp/results.jsonl",
				Records: []string{"run_summary", "window_summary", "run_error"},
				Rotate:  Rotate{MaxSize: 100 << 20, MaxFiles: 10},
			},
		},
	}
}

// Load reads and validates the file at path.
//
// A file that every user may write is accepted with a warning: whoever can
// change it controls what the daemon measures and where it listens.
func Load(path string) (*Config, error) {
	path = filepath.Clean(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o002 != 0 {
		c.warnings = append(c.warnings, Problem{Message: fmt.Sprintf(
			"%s is writable by every user (mode %v); restrict it, for example to 0640", path, fi.Mode().Perm())})
	}
	if err := c.checkSecretFiles(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Parse decodes, defaults and validates a configuration document. An empty
// document yields Default. A validation failure is an *Error listing every
// problem.
//
// The document is decoded twice: strictly into Default, which reports
// unknown keys with their lines, and then each session again on top of a
// copy of sender.defaults, so that the keys a session leaves out inherit.
//
//declscope:ignore overexported // the configuration API (Load / Parse) of the M2 task; gRPC session definitions parse bytes in M5
func Parse(b []byte) (*Config, error) {
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	var raw struct {
		Sender struct {
			Sessions []yaml.Node `yaml:"sessions"`
		} `yaml:"sender"`
	}
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	for i := range raw.Sender.Sessions {
		s := Session{TestParams: c.Sender.Defaults}
		if err := raw.Sender.Sessions[i].Decode(&s); err != nil {
			return nil, fmt.Errorf("yaml: sender.sessions[%d]: %w", i, err)
		}
		s.DropInherited(c.Sender.Defaults)
		c.Sender.Sessions[i] = s
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// DropInherited clears the values p inherited from defaults that do not
// apply to its mode: the Full extensions of a Light test, and STAMP of a
// Full one. So a Light session may inherit sender.defaults written for Full
// tests; a value the session sets itself stays, and Validate refuses it.
func (p *TestParams) DropInherited(defaults TestParams) {
	switch p.Mode {
	case "light":
		if slices.Equal(p.Extensions, defaults.Extensions) {
			p.Extensions = nil
		}
		if p.OctetsToReflect == defaults.OctetsToReflect {
			p.OctetsToReflect = 0
		}
		if p.PaddingToReflect == defaults.PaddingToReflect {
			p.PaddingToReflect = 0
		}
	case "full":
		if p.STAMP == defaults.STAMP && p.SSID == defaults.SSID {
			p.STAMP, p.SSID = false, 0
		}
	}
}

// Warnings returns the problems Validate found that do not stop the daemon.
func (c *Config) Warnings() []Problem { return c.warnings }
