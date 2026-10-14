//go:build integration && linux

package component

import (
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// topology is two network namespaces, A and B, joined by a veth pair. A holds
// veth "va" with addrA4 / addrA6, B holds "vb" with addrB4 / addrB6. Each side
// has a permanent neighbour entry for the other, so no packet waits for ARP
// or neighbour discovery: with netem on the link, the first packet would
// otherwise carry the resolution's round trip (ARP), or wait a second for a
// neighbour solicitation sent right after the link came up and lost
// (IPv6).
//
//declscope:package // the fixture every component test builds on
type topology struct {
	t      *testing.T
	a, b   string // namespace names
	addrA4 netip.Addr
	addrB4 netip.Addr
	addrA6 netip.Addr
	addrB6 netip.Addr
}

// requireRoot skips the test unless it can manage network namespaces.
//
//declscope:package // every component test calls it first
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("component tests need root: run make test-component")
	}
	for _, tool := range []string{"ip", "tc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
	}
}

// newTopology creates the namespaces and the veth pair, and removes them when
// the test ends.
//
//declscope:package // the fixture every component test builds on
func newTopology(t *testing.T) *topology {
	t.Helper()
	requireRoot(t)
	var r [4]byte
	_, _ = rand.Read(r[:])
	id := hex.EncodeToString(r[:])
	tp := &topology{
		t:      t,
		a:      "gotwamp-" + id + "-a",
		b:      "gotwamp-" + id + "-b",
		addrA4: netip.MustParseAddr("10.255.0.1"),
		addrB4: netip.MustParseAddr("10.255.0.2"),
		addrA6: netip.MustParseAddr("fd00:255::1"),
		addrB6: netip.MustParseAddr("fd00:255::2"),
	}
	run(t, "ip", "netns", "add", tp.a)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", tp.a).Run() })
	run(t, "ip", "netns", "add", tp.b)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", tp.b).Run() })

	const macA, macB = "02:67:74:77:00:0a", "02:67:74:77:00:0b"
	run(t, "ip", "link", "add", "va", "address", macA, "netns", tp.a,
		"type", "veth", "peer", "name", "vb", "address", macB, "netns", tp.b)
	for _, side := range []struct {
		ns, dev      string
		addr4, addr6 netip.Addr
		peer4, peer6 netip.Addr
		peerMAC      string
	}{
		{tp.a, "va", tp.addrA4, tp.addrA6, tp.addrB4, tp.addrB6, macB},
		{tp.b, "vb", tp.addrB4, tp.addrB6, tp.addrA4, tp.addrA6, macA},
	} {
		tp.exec(side.ns, "ip", "link", "set", "lo", "up")
		tp.exec(side.ns, "ip", "addr", "add", side.addr4.String()+"/24", "dev", side.dev)
		// nodad: the address is usable at once instead of after DAD.
		tp.exec(side.ns, "ip", "addr", "add", side.addr6.String()+"/64", "dev", side.dev, "nodad")
		tp.exec(side.ns, "ip", "link", "set", side.dev, "up")
		for _, peer := range []netip.Addr{side.peer4, side.peer6} {
			tp.exec(side.ns, "ip", "neigh", "replace", peer.String(), "lladdr", side.peerMAC,
				"dev", side.dev, "nud", "permanent")
		}
	}
	return tp
}

// exec runs a command inside namespace ns and returns its output.
func (tp *topology) exec(ns string, args ...string) string {
	tp.t.Helper()
	return run(tp.t, "ip", append([]string{"netns", "exec", ns}, args...)...)
}

// netem sets the netem qdisc on the egress of namespace ns's veth, for
// example netem(tp.a, "delay", "10ms") delays A→B. It fails the test unless
// tc then shows the netem qdisc on the device, and logs the qdisc with its
// counters when the test ends (shown when the test fails).
//
// Delay on the sender's own egress does not show in a TWAMP run that uses
// kernel transmit timestamps: the kernel takes T1' when the packet leaves
// the qdisc, after netem has held it (docs/ops/staging.md). Delay a TWAMP
// run on the reflector's side.
//
//declscope:package // each test picks its impairment
func (tp *topology) netem(ns string, params ...string) {
	tp.t.Helper()
	dev := "va"
	if ns == tp.b {
		dev = "vb"
	}
	tp.exec(ns, append([]string{"tc", "qdisc", "replace", "dev", dev, "root", "netem"}, params...)...)
	show := tp.exec(ns, "tc", "qdisc", "show", "dev", dev)
	logLines(tp.t, ns+" "+dev+" qdisc: ", show)
	if !strings.Contains(show, "qdisc netem") {
		tp.t.Fatalf("no netem qdisc on %s in %s after tc qdisc replace", dev, ns)
	}
	tp.t.Cleanup(func() {
		out, err := exec.Command("ip", "netns", "exec", ns, "tc", "-s", "qdisc", "show", "dev", dev).CombinedOutput()
		if err != nil {
			tp.t.Logf("tc -s qdisc show dev %s in %s: %v", dev, ns, err)
		}
		logLines(tp.t, ns+" "+dev+" qdisc at the end: ", string(out))
	})
}

// logLines logs text one line per call, each with the prefix, so that every
// line carries the file:line of the test (the CI failure report keeps only
// such lines).
//
//declscope:package // the tests log the output of tools through it
func logLines(t *testing.T, prefix, text string) {
	t.Helper()
	// A plain loop: the body of a range-over-func loop runs in a closure,
	// which would make the helper's own line the reported one.
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		t.Log(prefix + line)
	}
}

// in runs fn with the calling goroutine's thread switched into namespace ns.
// Sockets fn creates belong to ns for their whole life and may be used from
// any goroutine afterwards.
//
//declscope:package // tests open their sockets inside a namespace through it
func (tp *topology) in(ns string, fn func()) {
	tp.t.Helper()
	runtime.LockOSThread()
	orig, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		tp.t.Fatalf("open current netns: %v", err)
	}
	defer orig.Close()
	target, err := os.Open("/run/netns/" + ns)
	if err != nil {
		runtime.UnlockOSThread()
		tp.t.Fatalf("open netns %s: %v", ns, err)
	}
	defer target.Close()
	if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
		runtime.UnlockOSThread()
		tp.t.Fatalf("setns %s: %v", ns, err)
	}
	defer func() {
		// Leave the thread locked (and so discarded) if it cannot go back.
		if err := unix.Setns(int(orig.Fd()), unix.CLONE_NEWNET); err == nil {
			runtime.UnlockOSThread()
		}
	}()
	fn()
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		logLines(t, name+": ", string(out))
		t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return string(out)
}
