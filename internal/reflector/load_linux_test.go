//go:build load && linux

package reflector_test

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestLoadLight sends LOAD_PPS packets per second (default 50,000) for
// LOAD_SECONDS (default 10) to a Light reflector on loopback and reports the
// loss, the CPU time and the allocations per packet.
//
//	go test -tags load -run TestLoadLight -v ./internal/reflector/
//	LOAD_CPUPROFILE=cpu.out LOAD_PPS=100000 go test -tags load ...
func TestLoadLight(t *testing.T) {
	pps := envInt("LOAD_PPS", 50_000)
	seconds := envInt("LOAD_SECONDS", 10)

	light, err := reflector.ListenLight(reflector.Config{
		Listeners:  []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		ReadBuffer: 8 << 20,
	}, reflector.Deps{})
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error)
	go func() { served <- light.Serve(ctx) }()
	defer func() { cancel(); <-served }()
	target := light.Addrs()[0]

	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadBuffer(8 << 20)

	// maxGap is the longest silence between two replies once they started:
	// a stall of the reflector (or of this reader) longer than the socket
	// buffer lasts shows up here.
	var received, maxGap atomic.Int64
	go func() {
		buf := make([]byte, 2048)
		var last time.Time
		for {
			if _, _, err := client.ReadFromUDPAddrPort(buf); err != nil {
				return
			}
			now := time.Now()
			if !last.IsZero() && now.Sub(last) > time.Duration(maxGap.Load()) {
				maxGap.Store(int64(now.Sub(last)))
			}
			last = now
			received.Add(1)
		}
	}()

	if path := os.Getenv("LOAD_CPUPROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			t.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	var ms0, ms1 runtime.MemStats
	var ru0, ru1 syscall.Rusage
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
	wait0 := runDelay()

	// Absolute schedule in 1 ms slots: each slot sends its share at once.
	pkt := (&wire.SenderPacket{Padding: make([]byte, wire.DefaultPaddingLength)}).Append(nil)
	total := pps * seconds
	perSlot := pps / 1000
	start := time.Now()
	sent := 0
	for slot := 0; sent < total; slot++ {
		time.Sleep(time.Until(start.Add(time.Duration(slot) * time.Millisecond)))
		for i := 0; i < perSlot && sent < total; i++ {
			pkt[3], pkt[2], pkt[1], pkt[0] = byte(sent), byte(sent>>8), byte(sent>>16), byte(sent>>24)
			if _, err := client.WriteToUDPAddrPort(pkt, target); err != nil {
				t.Fatalf("send: %v", err)
			}
			sent++
		}
	}
	elapsed := time.Since(start)
	time.Sleep(500 * time.Millisecond) // the last replies

	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
	wait := runDelay() - wait0
	runtime.ReadMemStats(&ms1)
	cpu := time.Duration(ru1.Utime.Nano()+ru1.Stime.Nano()-ru0.Utime.Nano()-ru0.Stime.Nano()) * time.Nanosecond
	st := light.Status()
	got := uint64(received.Load())
	loss := 1 - float64(got)/float64(sent)
	allocs := float64(ms1.Mallocs-ms0.Mallocs) / float64(sent)
	t.Logf("sent %d in %v (%.0f pps); reflector received %d, reflected %d, send errors %d, dropped %v",
		sent, elapsed.Round(time.Millisecond), float64(sent)/elapsed.Seconds(),
		st.Counters.Received, st.Counters.Reflected, st.Counters.SendErrors, st.Counters.Dropped)
	t.Logf("replies %d, loss %.4f%%; process CPU %v (%.1f%% of one core, sender and receiver included); %.3f allocs per packet",
		got, 100*loss, cpu.Round(time.Millisecond), 100*cpu.Seconds()/elapsed.Seconds(), allocs)
	t.Logf("longest gap between replies %v; threads waited %v runnable but not running (host load %s)",
		time.Duration(maxGap.Load()).Round(time.Millisecond), wait.Round(time.Millisecond), loadavg())
	if loss > 0.01 {
		t.Errorf("loss %.2f%% above 1%%", 100*loss)
	}
}

// BenchmarkLightLoopback measures one request and reply through a Light
// reflector on loopback, client side included.
func BenchmarkLightLoopback(b *testing.B) {
	light, err := reflector.ListenLight(reflector.Config{
		Listeners: []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
	}, reflector.Deps{})
	if err != nil {
		b.Skipf("cannot listen: %v", err)
	}
	ctx, cancel := context.WithCancel(b.Context())
	served := make(chan error)
	go func() { served <- light.Serve(ctx) }()
	defer func() { cancel(); <-served }()
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	pkt := (&wire.SenderPacket{Padding: make([]byte, wire.DefaultPaddingLength)}).Append(nil)
	buf := make([]byte, 2048)
	target := light.Addrs()[0]
	b.ReportAllocs()
	for b.Loop() {
		if _, err := client.WriteToUDPAddrPort(pkt, target); err != nil {
			b.Fatal(err)
		}
		if _, _, err := client.ReadFromUDPAddrPort(buf); err != nil {
			b.Fatal(err)
		}
	}
}

// runDelay sums the time the threads of this process spent runnable but
// waiting for a CPU (the second field of /proc/self/task/*/schedstat).
// Threads that exited in between are missed, which only lowers the sum.
func runDelay() time.Duration {
	var sum time.Duration
	tasks, _ := filepath.Glob("/proc/self/task/*/schedstat")
	for _, p := range tasks {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if f := strings.Fields(string(b)); len(f) >= 2 {
			if ns, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				sum += time.Duration(ns)
			}
		}
	}
	return sum
}

// loadavg returns the 1-minute load average of the host.
func loadavg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if f := strings.Fields(string(b)); err == nil && len(f) > 0 {
		return f[0]
	}
	return "unknown"
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
