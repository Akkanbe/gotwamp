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
// veth "va" with addrA4 / addrA6, B holds "vb" with addrB4 / addrB6.
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

	run(t, "ip", "link", "add", "va", "netns", tp.a, "type", "veth", "peer", "name", "vb", "netns", tp.b)
	for _, side := range []struct {
		ns, dev      string
		addr4, addr6 netip.Addr
	}{
		{tp.a, "va", tp.addrA4, tp.addrA6},
		{tp.b, "vb", tp.addrB4, tp.addrB6},
	} {
		tp.exec(side.ns, "ip", "link", "set", "lo", "up")
		tp.exec(side.ns, "ip", "addr", "add", side.addr4.String()+"/24", "dev", side.dev)
		// nodad: the address is usable at once instead of after DAD.
		tp.exec(side.ns, "ip", "addr", "add", side.addr6.String()+"/64", "dev", side.dev, "nodad")
		tp.exec(side.ns, "ip", "link", "set", side.dev, "up")
	}
	return tp
}

// exec runs a command inside namespace ns.
func (tp *topology) exec(ns string, args ...string) {
	tp.t.Helper()
	run(tp.t, "ip", append([]string{"netns", "exec", ns}, args...)...)
}

// netem sets the netem qdisc on the egress of namespace ns's veth, for
// example netem(tp.a, "delay", "10ms") delays A→B.
//
//declscope:package // each test picks its impairment
func (tp *topology) netem(ns string, params ...string) {
	tp.t.Helper()
	dev := "va"
	if ns == tp.b {
		dev = "vb"
	}
	tp.exec(ns, append([]string{"tc", "qdisc", "replace", "dev", dev, "root", "netem"}, params...)...)
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

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
