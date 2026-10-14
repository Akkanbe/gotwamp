//go:build integration && linux

package component

import (
	"context"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// fullPair is a control server with the Full reflector in namespace B and a
// sender configuration for a Full run from namespace A.
type fullPair struct {
	tp     *topology
	server netip.AddrPort
	fc     sender.FullConfig
}

// startFull starts gotwampd's Full reflector side in B, listening on addr,
// and returns a FullConfig whose sockets open in A.
func startFull(t *testing.T, tp *topology, addrB netip.Addr) *fullPair {
	t.Helper()
	var ln net.Listener
	var err error
	tp.in(tp.b, func() { ln, err = net.Listen("tcp", netip.AddrPortFrom(addrB, 862).String()) })
	if err != nil {
		t.Fatalf("listen in B: %v", err)
	}
	// The reflector opens its session sockets later, from the server's
	// goroutines: switch to B for each.
	listenB := func(network string, local netip.AddrPort, opts tstamp.Options) (c tstamp.Conn, err error) {
		tp.in(tp.b, func() { c, err = tstamp.Listen(network, local, opts) })
		return c, err
	}
	full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{Listen: listenB})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{}, full, nil)
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
	p := &fullPair{tp: tp, server: netip.AddrPortFrom(addrB, 862)}
	p.fc = sender.FullConfig{
		Server: p.server,
		Dial: func(ctx context.Context, _ netip.Addr, s netip.AddrPort) (c net.Conn, err error) {
			var d net.Dialer
			tp.in(tp.a, func() { c, err = d.DialContext(ctx, "tcp", s.String()) })
			return c, err
		},
		Listen: func(network string, local netip.AddrPort, opts tstamp.Options) (c tstamp.Conn, err error) {
			tp.in(tp.a, func() { c, err = tstamp.Listen(network, local, opts) })
			return c, err
		},
	}
	return p
}

// TestFullAcrossNetem runs Full between namespaces with netem delaying and
// dropping A→B, and checks that the run sees the delay and the loss.
func TestFullAcrossNetem(t *testing.T) {
	tp := newTopology(t)
	tp.netem(tp.a, "delay", "10ms", "loss", "10%")
	for _, c := range []struct {
		name string
		a, b netip.Addr
	}{
		{"ipv4", tp.addrA4, tp.addrB4},
		{"ipv6", tp.addrA6, tp.addrB6},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := startFull(t, tp, c.b)
			cfg := sender.DefaultConfig(netip.AddrPort{})
			cfg.Count = 200
			cfg.Interval = 5 * time.Millisecond
			cfg.Timeout = time.Second
			sum, err := sender.RunFull(t.Context(), cfg, p.fc)
			if err != nil {
				t.Fatal(err)
			}
			if sum.Sent != 200 || sum.Lost == 0 || sum.Lost > 60 || sum.Received+sum.Lost != 200 {
				t.Errorf("sent %d received %d lost %d, want about 10%% loss", sum.Sent, sum.Received, sum.Lost)
			}
			// Loss on A→B only: the split puts it all in the forward direction.
			if sum.LossSplitValid && sum.LostReverse != 0 {
				t.Errorf("lost forward %d reverse %d", sum.LostForward, sum.LostReverse)
			}
			if sum.RTT.Min < 10*time.Millisecond || sum.RTT.Min > 15*time.Millisecond {
				t.Errorf("rtt min %v, want 10ms of netem delay", sum.RTT.Min)
			}
			if sum.Local.Addr() != c.a || sum.Remote.Addr() != c.b {
				t.Errorf("local %v remote %v", sum.Local, sum.Remote)
			}
		})
	}
}

// TestFullDecodedByTshark captures a Full run on the veth and checks that
// tshark decodes both TWAMP-Control and TWAMP-Test. It skips without tcpdump
// or tshark.
func TestFullDecodedByTshark(t *testing.T) {
	for _, tool := range []string{"tcpdump", "tshark"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	tp := newTopology(t)
	pcap := filepath.Join(t.TempDir(), "full.pcap")
	capture := exec.Command("ip", "netns", "exec", tp.a, "tcpdump", "-i", "va", "-U", "-w", pcap)
	if err := capture.Start(); err != nil {
		t.Fatalf("tcpdump: %v", err)
	}
	// A failing test must not leave tcpdump running; after the normal stop
	// below, these only return errors.
	t.Cleanup(func() {
		_ = capture.Process.Kill()
		_ = capture.Wait()
	})
	time.Sleep(500 * time.Millisecond) // let tcpdump attach

	p := startFull(t, tp, tp.addrB4)
	cfg := sender.DefaultConfig(netip.AddrPort{})
	cfg.Count = 5
	cfg.Interval = 10 * time.Millisecond
	cfg.Timeout = 500 * time.Millisecond
	sum, err := sender.RunFull(t.Context(), cfg, p.fc)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = capture.Process.Signal(os.Interrupt)
	_ = capture.Wait()

	port := strconv.Itoa(int(sum.Remote.Port()))
	out, err := exec.Command("tshark", "-r", pcap, "-d", "udp.port=="+port+",twamp.test").CombinedOutput()
	if err != nil {
		t.Fatalf("tshark: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{"TWAMP-Control", "TWAMP-Test"} {
		if !strings.Contains(text, want) {
			t.Errorf("tshark output has no %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Malformed") {
		t.Errorf("tshark reports a malformed packet:\n%s", text)
	}
}
