//go:build interop

package perfsonar_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Environment of the tests; make interop sets it up (see compose.yaml).
func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

var (
	container   = env("PERFSONAR_CONTAINER", "gotwamp-perfsonar")
	twampdPort  = env("PERFSONAR_TWAMPD_PORT", "862")
	gotwampPort = env("GOTWAMP_INTEROP_PORT", "18620")
)

// families are the address families the cases run over (PLAN §8.2).
var families = []struct {
	name string
	addr netip.Addr
}{
	{"ipv4", netip.MustParseAddr("127.0.0.1")},
	{"ipv6", netip.MustParseAddr("::1")},
}

// requirePerfSONAR skips the test unless the perfSONAR container runs.
func requirePerfSONAR(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", container).Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Skipf("perfSONAR container %q is not running: start it with make interop", container)
	}
}

func port(t *testing.T, s string) uint16 {
	t.Helper()
	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		t.Fatalf("port %q: %v", s, err)
	}
	return uint16(p)
}

// I-01, I-03: gotwamp sender → perfSONAR twampd, TWAMP Full.
func TestGotwampToTwampd(t *testing.T) {
	requirePerfSONAR(t)
	cases := []struct {
		name    string
		padding int
		dscp    int
		zero    bool
	}{
		{"defaults", sender.DefaultPadding, 0, false},
		{"padding 0", 0, 0, false},
		{"padding 1000 dscp ef", 1000, 46, false},
		{"zero addresses", sender.DefaultPadding, 0, true},
	}
	for _, f := range families {
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				cfg := sender.DefaultConfig(netip.AddrPort{})
				cfg.Count = 100
				cfg.Interval = 20 * time.Millisecond
				cfg.Padding = c.padding
				cfg.DSCP = c.dscp
				var mu sync.Mutex
				var tos []int
				hooks := sender.WithHooks(sender.Hooks{Packet: func(r sender.PacketResult) {
					mu.Lock()
					tos = append(tos, r.TOS)
					mu.Unlock()
				}})
				fc := sender.FullConfig{Server: netip.AddrPortFrom(f.addr, port(t, twampdPort)), ZeroAddresses: c.zero}
				sum, err := sender.RunFull(t.Context(), cfg, fc, hooks)
				if err != nil {
					t.Fatal(err)
				}
				if sum.Sent != 100 || sum.Received != 100 || sum.Lost != 0 || sum.DupForward+sum.DupReverse != 0 {
					t.Errorf("sent %d received %d lost %d dup %d/%d", sum.Sent, sum.Received, sum.Lost, sum.DupForward, sum.DupReverse)
				}
				if sum.RTT.Median <= 0 || sum.RTT.Median > 20*time.Millisecond || sum.ReflectorDelay.Min < 0 {
					t.Errorf("rtt %+v reflector delay %+v", sum.RTT, sum.ReflectorDelay)
				}
				// twampd reflects the Sender TTL it read; on loopback no hop is crossed.
				if sum.ForwardHops.Max != 0 || sum.ReverseHops.Max != 0 {
					t.Errorf("hops forward %+v reverse %+v", sum.ForwardHops, sum.ReverseHops)
				}
				// The requested Receiver Port (the Sender Port) is outside
				// twampd's testports, so it offers one of 18760-19960.
				if p := sum.Remote.Port(); p < 18760 || p > 19960 {
					t.Errorf("reflector port %d outside twampd's testports", p)
				}
				for i, v := range tos {
					if v >= 0 && v>>2 != c.dscp {
						t.Errorf("reply %d: DSCP %d, want %d (RFC 5357 §3.5)", i, v>>2, c.dscp)
						break
					}
				}
				t.Logf("rtt min/median/max %v/%v/%v, reflector %v synced %v", sum.RTT.Min, sum.RTT.Median, sum.RTT.Max, sum.Remote, sum.ReflectorSynced)
			})
		}
	}
}

// I-04 (part): twampd's alternate port is refused when the client asks so.
func TestGotwampRejectsAlternatePort(t *testing.T) {
	requirePerfSONAR(t)
	cfg := sender.DefaultConfig(netip.AddrPort{})
	cfg.Count = 1
	fc := sender.FullConfig{Server: netip.AddrPortFrom(families[0].addr, port(t, twampdPort)), RejectAlternatePort: true}
	_, err := sender.RunFull(t.Context(), cfg, fc)
	var ce *client.Error
	if !errors.As(err, &ce) || ce.Stage != client.StageRequest || !errors.Is(err, client.ErrAlternatePort) {
		t.Fatalf("err = %v, want an alternate port refusal", err)
	}
}

// recording wraps the Full reflector and keeps every SessionSpec it opens.
type recording struct {
	server.SessionHandler
	full   *reflector.Full
	opened []time.Time
	mu     sync.Mutex
	specs  []server.SessionSpec
}

func (r *recording) Open(spec server.SessionSpec) (server.Session, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.opened = append(r.opened, time.Now())
	r.mu.Unlock()
	return r.SessionHandler.Open(spec)
}

func (r *recording) last() server.SessionSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.specs[len(r.specs)-1]
}

// startGotwampd runs gotwampd's TWAMP-Control server with the Full reflector
// on addr:GOTWAMP_INTEROP_PORT.
func startGotwampd(t *testing.T, addr netip.Addr, cfg server.Config) (netip.AddrPort, *recording) {
	t.Helper()
	ap := netip.AddrPortFrom(addr, port(t, gotwampPort))
	ln, err := net.Listen("tcp", ap.String())
	if err != nil {
		t.Fatalf("listen %v: %v", ap, err)
	}
	full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recording{SessionHandler: full, full: full}
	srv, err := server.New(cfg, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return ap, rec
}

// reflectorView is what the Full reflector did during one twping run.
type reflectorView struct {
	received, reflected, sendErrors uint64
	dropped                         []string
	startLead, timeout              time.Duration
}

// view returns what the reflector did since before, once it has counted
// the replies of the packets it received: it counts a reply after sending
// it, so twping may have it first.
func (r *recording) view(before reflector.Counters) reflectorView {
	// Every packet received is, once handled, reflected, failed to send or
	// dropped.
	handled := func(a reflector.Counters) bool {
		got := a.Received - before.Received
		done := a.Reflected - before.Reflected + a.SendErrors - before.SendErrors + sumDropped(a) - sumDropped(before)
		return done >= got
	}
	after := r.full.Counters()
	for deadline := time.Now().Add(2 * time.Second); !handled(after) && time.Now().Before(deadline); after = r.full.Counters() {
		time.Sleep(10 * time.Millisecond)
	}
	v := reflectorView{
		received:   after.Received - before.Received,
		reflected:  after.Reflected - before.Reflected,
		sendErrors: after.SendErrors - before.SendErrors,
	}
	for _, reason := range reflector.DropReasons() {
		if d := after.Dropped[reason] - before.Dropped[reason]; d > 0 {
			v.dropped = append(v.dropped, fmt.Sprintf("%v=%d", reason, d))
		}
	}
	r.mu.Lock()
	v.startLead = r.specs[len(r.specs)-1].StartTime.Sub(r.opened[len(r.opened)-1])
	v.timeout = r.specs[len(r.specs)-1].Timeout
	r.mu.Unlock()
	return v
}

func sumDropped(c reflector.Counters) uint64 {
	var n uint64
	for _, d := range c.Dropped {
		n += d
	}
	return n
}

// clean reports whether the reflector answered each of sent packets.
func (v reflectorView) clean(sent uint64) bool {
	return v.received == sent && v.reflected == sent && v.sendErrors == 0 && len(v.dropped) == 0
}

func (v reflectorView) String() string {
	return fmt.Sprintf("reflector received %d, reflected %d, send errors %d, dropped %v (Start Time %v after the request, Timeout %v)",
		v.received, v.reflected, v.sendErrors, v.dropped, v.startLead, v.timeout)
}

// twpingToGotwampd runs twping against gotwampd and judges any loss by what
// the reflector did. A loss gotwampd caused (a packet dropped, unanswered or
// not sent) fails the test. A loss of replies gotwampd did send happens on
// twping's side: twping reads replies between its sends and declares a
// packet lost once its loss timeout (-L, 2 s) has passed, so a stall of the
// twping process on a loaded host loses replies that arrived. Such a run is
// retried once and fails only if the loss repeats.
func twpingToGotwampd(t *testing.T, rec *recording, args ...string) map[string]string {
	t.Helper()
	for attempt := 1; ; attempt++ {
		before := rec.full.Counters()
		kv := twping(t, args...)
		v := rec.view(before)
		if kv["LOST"] != "0" {
			t.Logf("twping lost %s of %s; %v", kv["LOST"], kv["SENT"], v)
		}
		retry, err := judgeTwping(kv, v, attempt)
		if err != nil {
			t.Error(err)
		}
		if !retry {
			return kv
		}
		t.Logf("gotwampd answered every packet: the loss is twping's (its loss timeout on a loaded host); retrying once")
	}
}

// judgeTwping decides a twping run by its summary kv and what the reflector
// did, v: whether to retry it, and the error to fail with.
func judgeTwping(kv map[string]string, v reflectorView, attempt int) (retry bool, err error) {
	if kv["LOST"] == "0" {
		return false, nil
	}
	sent, _ := strconv.ParseUint(kv["SENT"], 10, 64)
	switch {
	case !v.clean(sent):
		return false, fmt.Errorf("gotwampd did not answer every packet twping sent: %v", v)
	case attempt >= 2:
		return false, fmt.Errorf("twping lost %s replies again, although gotwampd answered every packet", kv["LOST"])
	default:
		return true, nil
	}
}

// twping runs perfSONAR's twping in the container with -M and returns the
// machine-readable summary as key/value pairs.
func twping(t *testing.T, args ...string) map[string]string {
	t.Helper()
	cmdArgs := append([]string{"exec", container, "twping", "-M"}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("twping %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "\t"); ok && k != "" {
			kv[k] = v
		}
	}
	if kv["SENT"] == "" {
		t.Fatalf("twping printed no summary:\n%s", out)
	}
	return kv
}

func float(t *testing.T, kv map[string]string, key string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(kv[key], 64)
	if err != nil {
		t.Fatalf("%s = %q: %v", key, kv[key], err)
	}
	return v
}

// I-02: perfSONAR twping → gotwampd (Full server and reflector).
func TestTwpingToGotwampd(t *testing.T) {
	requirePerfSONAR(t)
	cases := []struct {
		name string
		args []string
		dscp uint8
		pad  uint32
	}{
		{"defaults", nil, 0, 27},
		{"padding 0", []string{"-s", "0"}, 0, 0},
		{"padding 1000", []string{"-s", "1000"}, 0, 1000},
		{"dscp ef", []string{"-D", "46"}, 46, 27},
		{"zero addresses", []string{"-Z"}, 0, 27},
	}
	for _, f := range families {
		ap, rec := startGotwampd(t, f.addr, server.Config{})
		for _, c := range cases {
			t.Run(f.name+"/"+c.name, func(t *testing.T) {
				args := append([]string{"-c", "100", "-i", "0.02"}, c.args...)
				kv := twpingToGotwampd(t, rec, append(args, ap.String())...)
				if kv["SENT"] != "100" || kv["DUPS_FWD"] != "0" || kv["DUPS_BCK"] != "0" {
					t.Errorf("SENT %s DUPS %s/%s", kv["SENT"], kv["DUPS_FWD"], kv["DUPS_BCK"])
				}
				if med := float(t, kv, "MEDIAN"); med <= 0 || med > 0.02 {
					t.Errorf("MEDIAN %v s", med)
				}
				// gotwampd reflects the TTL it received (255 from twping) and
				// sends with 255: no hop either way on loopback.
				if kv["MINTTL_FWD"] != "255" || kv["MINTTL_BCK"] != "255" {
					t.Errorf("TTL fwd %s bck %s", kv["MINTTL_FWD"], kv["MINTTL_BCK"])
				}
				spec := rec.last()
				if spec.DSCP != c.dscp || spec.PaddingLength != c.pad {
					t.Errorf("session spec %+v, want dscp %d padding %d", spec, c.dscp, c.pad)
				}
				t.Logf("twping: median %s s, proc %s..%s s", kv["MEDIAN"], kv["MIN_PROC"], kv["MAX_PROC"])
			})
		}
	}
}

// The shared secret of the authenticated cases: the TEST ONLY keys of
// conf/twamp-server-test-only.pfs, which twampd and twping read too (mounted
// as /etc/gotwamp-interop/twamp-server.pfs).
const (
	pfsIdentity  = "gotwamp"
	pfsContainer = "/etc/gotwamp-interop/twamp-server.pfs"
)

func pfsKeys(t *testing.T) keystore.Map {
	t.Helper()
	keys, err := keystore.LoadPFS("conf/twamp-server-test-only.pfs")
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// securityModes are the protected modes, with twping's letter for each.
var securityModes = []struct {
	mode   wire.SecurityMode
	letter string
}{
	{wire.SecurityAuthenticated, "A"},
	{wire.SecurityEncrypted, "E"},
	{wire.SecurityMixed, "M"},
}

// I-05: gotwamp sender → perfSONAR twampd in the authenticated, encrypted
// and mixed modes (M7-1).
func TestGotwampToTwampdSecurity(t *testing.T) {
	requirePerfSONAR(t)
	pass, _ := pfsKeys(t).Passphrase(pfsIdentity)
	for _, f := range families {
		for _, m := range securityModes {
			for _, padding := range []int{sender.DefaultPadding, 1000} {
				t.Run(fmt.Sprintf("%s/%v/padding %d", f.name, m.mode, padding), func(t *testing.T) {
					cfg := sender.DefaultConfig(netip.AddrPort{})
					cfg.Count = 100
					cfg.Interval = 20 * time.Millisecond
					cfg.Padding = padding
					fc := sender.FullConfig{
						Server: netip.AddrPortFrom(f.addr, port(t, twampdPort)),
						Modes:  m.mode.Mode(), KeyID: pfsIdentity, Passphrase: pass,
					}
					sum, err := sender.RunFull(t.Context(), cfg, fc)
					if err != nil {
						t.Fatal(err)
					}
					if sum.Sent != 100 || sum.Received != 100 || sum.Lost != 0 {
						t.Errorf("sent %d received %d lost %d", sum.Sent, sum.Received, sum.Lost)
					}
					if sum.RTT.Median <= 0 || sum.RTT.Median > 20*time.Millisecond || sum.ReflectorDelay.Min < 0 {
						t.Errorf("rtt %+v reflector delay %+v", sum.RTT, sum.ReflectorDelay)
					}
					if sum.ForwardHops.Max != 0 || sum.ReverseHops.Max != 0 {
						t.Errorf("hops forward %+v reverse %+v", sum.ForwardHops, sum.ReverseHops)
					}
					t.Logf("rtt min/median/max %v/%v/%v", sum.RTT.Min, sum.RTT.Median, sum.RTT.Max)
				})
			}
		}
	}
	t.Run("wrong passphrase", func(t *testing.T) {
		fc := sender.FullConfig{
			Server: netip.AddrPortFrom(families[0].addr, port(t, twampdPort)),
			Modes:  wire.ModeEncrypted, KeyID: pfsIdentity, Passphrase: []byte("not the secret"),
		}
		_, err := sender.RunFull(t.Context(), sender.DefaultConfig(netip.AddrPort{}), fc)
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Stage != client.StageSetup || ce.Accept == wire.AcceptOK {
			t.Errorf("err = %v, want a refusal at setup", err)
		}
	})
}

// I-06: perfSONAR twping → gotwampd in the authenticated, encrypted and
// mixed modes (M7-1).
func TestTwpingToGotwampdSecurity(t *testing.T) {
	requirePerfSONAR(t)
	all := wire.SecurityModes
	for _, f := range families {
		ap, rec := startGotwampd(t, f.addr, server.Config{Modes: all, Keys: pfsKeys(t)})
		for _, m := range securityModes {
			for _, padding := range []string{"27", "1000"} {
				t.Run(fmt.Sprintf("%s/%v/padding %s", f.name, m.mode, padding), func(t *testing.T) {
					kv := twpingToGotwampd(t, rec, "-c", "100", "-i", "0.02", "-s", padding, "-A", m.letter, "-u", pfsIdentity, "-k", pfsContainer, ap.String())
					if kv["SENT"] != "100" || kv["DUPS_FWD"] != "0" || kv["DUPS_BCK"] != "0" {
						t.Errorf("SENT %s DUPS %s/%s", kv["SENT"], kv["DUPS_FWD"], kv["DUPS_BCK"])
					}
					if med := float(t, kv, "MEDIAN"); med <= 0 || med > 0.02 {
						t.Errorf("MEDIAN %v s", med)
					}
					if kv["MINTTL_FWD"] != "255" || kv["MINTTL_BCK"] != "255" {
						t.Errorf("TTL fwd %s bck %s", kv["MINTTL_FWD"], kv["MINTTL_BCK"])
					}
					if spec := rec.last(); spec.Security != m.mode.TestMode() || strconv.Itoa(int(spec.PaddingLength)) != padding {
						t.Errorf("session spec %+v, want test mode %v", spec, m.mode.TestMode())
					}
					t.Logf("twping: median %s s, proc %s..%s s", kv["MEDIAN"], kv["MIN_PROC"], kv["MAX_PROC"])
				})
			}
		}
	}
}

// I-06 (part): gotwampd refuses twping when the secrets differ.
func TestTwpingWrongPassphrase(t *testing.T) {
	requirePerfSONAR(t)
	all := wire.SecurityModes
	ap, rec := startGotwampd(t, families[0].addr, server.Config{Modes: all, Keys: keystore.Map{pfsIdentity: []byte("another secret")}})
	out, err := exec.Command("docker", "exec", container, "twping", "-c", "1", "-A", "E", "-u", pfsIdentity, "-k", pfsContainer, ap.String()).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "denied") {
		t.Errorf("twping with the wrong secret: err %v\n%s", err, out)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.specs) != 0 {
		t.Errorf("sessions opened: %+v", rec.specs)
	}
}

// TestJudgeTwping checks how a twping loss is judged; it needs no container.
func TestJudgeTwping(t *testing.T) {
	clean := reflectorView{received: 100, reflected: 100}
	for _, tt := range []struct {
		name    string
		lost    string
		v       reflectorView
		attempt int
		retry   bool
		fails   bool
	}{
		{"no loss", "0", clean, 1, false, false},
		{"twping's loss is retried", "1", clean, 1, true, false},
		{"twping's loss twice fails", "1", clean, 2, false, true},
		{"packet never reached gotwampd", "1", reflectorView{received: 99, reflected: 99}, 1, false, true},
		{"gotwampd dropped one", "1", reflectorView{received: 100, reflected: 99, dropped: []string{"not_started=1"}}, 1, false, true},
		{"gotwampd failed to send one", "1", reflectorView{received: 100, reflected: 99, sendErrors: 1}, 1, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			retry, err := judgeTwping(map[string]string{"SENT": "100", "LOST": tt.lost}, tt.v, tt.attempt)
			if retry != tt.retry || (err != nil) != tt.fails {
				t.Errorf("judgeTwping = %v, %v; want retry %v, fails %v", retry, err, tt.retry, tt.fails)
			}
		})
	}
}
