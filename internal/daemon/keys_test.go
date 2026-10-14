package daemon

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// writePFS writes a keys file holding identity alice.
func writePFS(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys.pfs")
	if err := os.WriteFile(path, []byte("# test\nalice "+hex.EncodeToString([]byte("secret"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionSecurityKeys: a monitoring session in a security mode starts
// with its key from sender.keys_file, and fails to start without the file
// or with a KeyID the file lacks.
func TestSessionSecurityKeys(t *testing.T) {
	keys := writePFS(t)
	for _, tc := range []struct {
		name, keysFile, keyID, wantErr string
	}{
		{"no keys file", "", "alice", "security authenticated needs sender.keys_file"},
		{"unknown key", keys, "bob", `key_id "bob" is not in sender.keys_file`},
		{"known key", keys, "alice", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(t)
			c.Sender.KeysFile = tc.keysFile
			p := c.Sender.Defaults
			p.Security, p.KeyID = "authenticated", tc.keyID
			c.Sender.Sessions = []config.Session{{Name: "auth", Target: "127.0.0.1", Schedule: config.Schedule{Type: "periodic", Frequency: time.Hour, Jitter: time.Hour}, TestParams: p}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			d, err := Start(ctx, c, Options{})
			if err == nil {
				cancel()
				_ = d.Wait()
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Start: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Start = %v, want %q", err, tc.wantErr)
			}
		})
	}
	// A KeyID in sender.defaults that the file lacks fails too, so that
	// ad-hoc tests do not fail later one by one.
	c := testConfig(t)
	c.Sender.KeysFile = keys
	c.Sender.Defaults.Security, c.Sender.Defaults.KeyID = "encrypted", "carol"
	if _, err := loadKeys(c); err == nil || !strings.Contains(err.Error(), `sender.defaults: key_id "carol"`) {
		t.Errorf("defaults with an unknown key: %v", err)
	}
	c.Sender.KeysFile = filepath.Join(t.TempDir(), "none.pfs")
	if _, err := loadKeys(c); err == nil || !strings.Contains(err.Error(), "sender.keys_file") {
		t.Errorf("missing keys file: %v", err)
	}
}

// TestRunTestSecurity: ad-hoc Full tests in each security mode, from the
// daemon's sender to its own control server, both reading one keys file.
func TestRunTestSecurity(t *testing.T) {
	keys := writePFS(t)
	c := testConfig(t)
	c.Sender.KeysFile = keys
	c.Reflector.Control.KeysFile = keys
	r := start(t, c, Options{})
	defer r.stop(t)
	for _, mode := range []gotwampv1.SecurityMode{
		gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED,
		gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED,
		gotwampv1.SecurityMode_SECURITY_MODE_MIXED,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			spec := &gotwampv1.TestSpec{
				Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(r.d.ControlAddrs()[0].Port()),
				Count: proto.Uint32(3), Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(time.Second),
				Security: mode, KeyId: "alice",
			}
			stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
			if err != nil {
				t.Fatal(err)
			}
			evs := collect(t, stream)
			sum := evs[len(evs)-1].GetSummary()
			if sum.GetReceived() != 3 || sum.GetSpec().GetSecurity() != mode {
				t.Fatalf("summary = %v (events %v)", sum, evs)
			}
		})
	}
	if n := strings.Count(r.logs.String(), "client authenticated"); n != 3 {
		t.Errorf("%d authenticated clients logged, want 3", n)
	}
	// A wrong key is refused by the control server: a protocol error.
	spec := &gotwampv1.TestSpec{
		Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(r.d.ControlAddrs()[0].Port()),
		Count: proto.Uint32(1), Security: gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED, KeyId: "bob",
	}
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil || !strings.Contains(err.Error(), `key_id "bob" is not in sender.keys_file`) {
		t.Errorf("unknown key: %v", err)
	}
}

func TestOfferedModes(t *testing.T) {
	if m := offeredModes(config.ControlServer{KeysFile: "/k"}); m != wire.SecurityModes {
		t.Errorf("modes with keys = %v", m)
	}
	if m := offeredModes(config.ControlServer{}); m != wire.ModeUnauthenticated {
		t.Errorf("modes without keys = %v", m)
	}
}

// TestHardwareSockets: in hardware mode the opener names the interface of
// a wildcard socket, from the configuration or the route to the target,
// and logs each outcome once. The loopback device has no card, so the
// sockets fall back to kernel timestamps.
func TestHardwareSockets(t *testing.T) {
	var logs syncBuffer
	open := &socketOpener{log: slog.New(slog.NewTextHandler(&logs, nil))}
	opts := tstamp.Options{Mode: tstamp.ModeHardware}
	wildcard := netip.MustParseAddrPort("0.0.0.0:0")
	for range 2 {
		c, err := open.listenTo("udp4", wildcard, opts, netip.MustParseAddr("127.0.0.1"))
		if err != nil {
			t.Fatal(err)
		}
		if hw := c.(tstamp.HardwareReporter).Hardware(); hw.Interface != "lo" || hw.Err == nil || c.Mode() != tstamp.ModeKernel {
			t.Errorf("route: mode %v, hardware %+v", c.Mode(), hw)
		}
		_ = c.Close()
	}
	if n := strings.Count(logs.String(), "hardware timestamping unavailable"); n != 1 {
		t.Errorf("%d warnings for one outcome, want 1:\n%s", n, logs.String())
	}
	open.iface = "lo"
	c, err := open.listen("udp4", wildcard, opts)
	if err != nil {
		t.Fatal(err)
	}
	if hw := c.(tstamp.HardwareReporter).Hardware(); hw.Interface != "lo" {
		t.Errorf("configured interface: %+v", hw)
	}
	_ = c.Close()
	if timestampingMode("hardware") != tstamp.ModeHardware || protoTimestamping(tstamp.ModeHardware) != gotwampv1.TimestampingMode_TIMESTAMPING_MODE_HARDWARE {
		t.Error("hardware mode not mapped")
	}
	// Without a card, the daemon reports what its sockets get.
	if m := timestampingInEffect(nil, "hardware", "lo"); m != tstamp.ModeKernel {
		t.Errorf("in effect = %v, want kernel on the loopback", m)
	}
}

// TestReloadSecurityKeys: a reload checks the KeyIDs of new sessions
// against the keys read at startup.
func TestReloadSecurityKeys(t *testing.T) {
	keys := writePFS(t)
	c := testConfig(t)
	c.Sender.KeysFile = keys
	path := filepath.Join(t.TempDir(), "gotwampd.yaml")
	write := func(keyID string) {
		t.Helper()
		doc := "sender:\n  enabled: true\n  keys_file: " + keys + "\n  sessions:\n" +
			"    - {name: s, target: 127.0.0.1, security: encrypted, key_id: " + keyID + ", schedule: {frequency: 1h, jitter: 1h}}\n"
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := start(t, c, Options{ConfigPath: path})
	defer r.stop(t)
	client := gotwampv1.NewDaemonServiceClient(r.conn)
	write("bob")
	if _, err := client.ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{}); err == nil || !strings.Contains(err.Error(), `key_id "bob"`) {
		t.Errorf("reload with an unknown key: %v", err)
	}
	write("alice")
	resp, err := client.ReloadConfig(t.Context(), &gotwampv1.ReloadConfigRequest{})
	if err != nil || len(resp.GetSessionsAdded()) != 1 {
		t.Errorf("reload with a known key: %v, %v", resp, err)
	}
}
