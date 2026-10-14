//go:build soak && linux

package soak

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/export/jsonl"
	"github.com/Akkanbe/gotwamp/internal/export/prom"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/store/sqlite"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

var (
	flagSessions = flag.Int("soak.sessions", 20, "monitoring sessions; even ones continuous, odd ones periodic")
	flagPPS      = flag.Int("soak.pps", 10, "packets per second of each session")
	flagDuration = flag.Duration("soak.duration", 60*time.Second, "how long the sessions run")
	flagWindow   = flag.Duration("soak.window", 10*time.Second, "window of continuous sessions and frequency of periodic ones")
	flagSample   = flag.Duration("soak.sample", 10*time.Second, "sampling period of the resource figures")
	flagSpin     = flag.Duration("soak.spin", 0, "sender spin_before; 0 is the default 200µs, negative disables spinning")
	flagMode     = flag.String("soak.timestamping", "auto", "auto, kernel or userspace")
	flagPackets  = flag.Bool("soak.jsonl-packets", true, "also write per-packet JSON Lines records")
	flagCSV      = flag.String("soak.csv", "", "write the samples to this CSV file")
	flagStore    = flag.String("soak.store", "memory", "result store: memory, or sqlite in a temporary directory")

	flagGoroutineGrowth = flag.Int("soak.max-goroutine-growth", 10, "goroutines allowed to appear between the middle and the end of the run")
	flagHeapGrowth      = flag.Float64("soak.max-heap-growth", 0.25, "live heap growth allowed between the middle and the end, as a fraction")
	flagHeapSlack       = flag.Int64("soak.heap-slack", 8<<20, "live heap growth always allowed, in octets")
	flagMaxLoss         = flag.Float64("soak.max-loss", 0.01, "largest acceptable loss ratio on loopback")
	flagMaxCPU          = flag.Float64("soak.max-cpu", 50, "largest average CPU, in percent of one core (SPEC §12.1: 100 sessions × 10 pps < 50%)")
	flagMaxRSS          = flag.Uint64("soak.max-rss", 200<<20, "largest RSS in octets (SPEC §12.1: 100 sessions < 200 MiB)")
)

// sample is one reading of the process's resources.
type sample struct {
	at         time.Duration // since the sessions started
	goroutines int
	liveHeap   uint64 // live heap after the last GC
	heapInuse  uint64
	rss        uint64
	fds        int
	cpu        time.Duration // user + system since the process started
	packets    uint64
	summaries  uint64
	db         uint64 // size of the SQLite database and its WAL; 0 in memory
}

// dbFile is the SQLite database of -soak.store sqlite, empty in memory.
var dbFile string

// dbSize returns the size of dbFile and its WAL.
func dbSize() uint64 {
	if dbFile == "" {
		return 0
	}
	var n uint64
	for _, f := range []string{dbFile, dbFile + "-wal"} {
		if fi, err := os.Stat(f); err == nil {
			n += uint64(fi.Size()) //nolint:gosec // G115: a file size
		}
	}
	return n
}

func take(start time.Time, c *counter) sample {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	live := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(live)
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return sample{
		at:         time.Since(start).Round(time.Second),
		goroutines: runtime.NumGoroutine(),
		liveHeap:   live[0].Value.Uint64(),
		heapInuse:  ms.HeapInuse,
		rss:        rss(),
		fds:        fds(),
		cpu:        time.Duration(ru.Utime.Nano() + ru.Stime.Nano()),
		packets:    c.packets.Load(),
		summaries:  c.summaries.Load(),
		db:         dbSize(),
	}
}

func rss() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "VmRSS:" {
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

func fds() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

func mib(v uint64) string { return fmt.Sprintf("%.1fMiB", float64(v)/(1<<20)) }

// counter is a sink that totals the results.
type counter struct {
	packets, summaries, aborted, errors, skipped atomic.Uint64
	// kept counts what a store keeps: summaries, aborted ones included,
	// and run errors of the sessions.
	kept                 atomic.Uint64
	sent, received, lost atomic.Uint64
	lastError            atomic.Pointer[string]
}

func (c *counter) Publish(e store.Event) {
	switch e.Kind {
	case store.EventPacket:
		c.packets.Add(1)
	case store.EventRunSummary, store.EventWindowSummary:
		c.kept.Add(1)
		if e.Summary.Aborted {
			c.aborted.Add(1)
			return
		}
		c.summaries.Add(1)
		c.sent.Add(e.Summary.Sent)
		c.received.Add(e.Summary.Received)
		c.lost.Add(e.Summary.Lost)
	case store.EventRunError:
		c.kept.Add(1)
		c.errors.Add(1)
		msg := e.Session.Name + ": " + e.Error.Stage + ": " + e.Error.Message
		c.lastError.Store(&msg)
	case store.EventRunSkipped:
		c.skipped.Add(1)
	}
}

func timestamping(t *testing.T) tstamp.Mode {
	switch *flagMode {
	case "auto":
		return tstamp.ModeAuto
	case "kernel":
		return tstamp.ModeKernel
	case "userspace":
		return tstamp.ModeUserspace
	}
	t.Fatalf("unknown -soak.timestamping %q", *flagMode)
	return 0
}

// specs builds the sessions: even ones continuous, odd ones periodic runs
// that leave the timeout and a second free before the next start.
func specs(t *testing.T, target netip.AddrPort) []schedule.SessionSpec {
	interval := time.Second / time.Duration(*flagPPS)
	var out []schedule.SessionSpec
	for i := range *flagSessions {
		test := sender.DefaultConfig(target)
		test.Interval = interval
		test.Timeout = min(sender.DefaultTimeout, *flagWindow/4)
		test.SpinBefore = *flagSpin
		test.Timestamping = timestamping(t)
		test.MaxSamples = 10_000
		sc := schedule.Schedule{Type: schedule.Continuous, Window: *flagWindow, Jitter: time.Second}
		busy := *flagWindow - test.Timeout - time.Second
		if i%2 == 1 && busy >= interval {
			test.Count = uint32(busy / interval)
			sc = schedule.Schedule{Type: schedule.Periodic, Frequency: *flagWindow, Jitter: *flagWindow}
		}
		name := fmt.Sprintf("soak-%03d", i)
		out = append(out, schedule.SessionSpec{
			Name:     name,
			Schedule: sc,
			Info: store.SessionInfo{
				Target: target.String(), Mode: "light", Labels: map[string]string{"group": strconv.Itoa(i % 4)},
				Count: test.Count, Interval: interval, Padding: test.Padding,
			},
			Test: test,
		})
	}
	return out
}

func TestSoak(t *testing.T) {
	if *flagSessions < 1 || *flagPPS < 1 || *flagDuration < 2**flagSample {
		t.Fatal("need at least one session, 1 pps, and a duration of two samples")
	}
	runtime.GC()
	base := take(time.Now(), &counter{})

	// The reflector, on loopback.
	refl, err := reflector.ListenLight(reflector.Config{
		Listeners:    []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:0")},
		Timestamping: timestamping(t),
	}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	reflCtx, stopRefl := context.WithCancel(context.Background())
	reflDone := make(chan error, 1)
	go func() { reflDone <- refl.Serve(reflCtx) }()
	target := refl.Addrs()[0]

	// The result path: store, Prometheus, JSON Lines, and a counter.
	history := max(3, int(*flagDuration / *flagWindow / 4))
	var (
		st store.Store
		db *sqlite.Store
	)
	switch *flagStore {
	case "memory":
		st = store.NewMemory(history, 0)
	case "sqlite":
		dbFile = filepath.Join(t.TempDir(), "results.db")
		db, err = sqlite.Open(sqlite.Config{
			Path: dbFile, History: history,
			Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		})
		if err != nil {
			t.Fatal(err)
		}
		st = db
	default:
		t.Fatalf("unknown -soak.store %q", *flagStore)
	}
	records := jsonl.DefaultRecords
	if *flagPackets {
		records |= jsonl.Packet
	}
	jw, err := jsonl.Open(jsonl.Config{
		Path: filepath.Join(t.TempDir(), "results.jsonl"), Records: records, MaxSize: 16 << 20, MaxFiles: 3, Daily: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	col := prom.New(prom.Config{
		Reflector: prom.ReflectorSourceFunc(func() prom.ReflectorSnapshot {
			st := refl.Status()
			return prom.ReflectorSnapshot{
				Listeners:          []prom.ReflectorListener{{Kind: "light", Listener: target.String(), Received: st.Counters.Received, Reflected: st.Counters.Reflected}},
				SessionsActive:     map[string]int{"light": st.FlowsActive},
				LightFlowsOverflow: st.FlowsOverflow,
			}
		}),
		Timestamping: *flagMode,
		JSONLDropped: jw.Dropped,
	})
	cnt := &counter{}

	// A watcher and a scraper, as the API and Prometheus would be.
	watch := st.Watch(store.WatchOptions{})
	var aux sync.WaitGroup
	aux.Go(func() {
		for range watch.C() {
		}
	})
	srv := httptest.NewServer(col.Handler())
	scrapeCtx, stopScrape := context.WithCancel(context.Background())
	var scrapes, scrapeErrors atomic.Uint64
	aux.Go(func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-scrapeCtx.Done():
				return
			case <-tick.C:
			}
			resp, err := http.Get(srv.URL + "/metrics")
			if err == nil {
				_, err = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("status %d", resp.StatusCode)
				}
			}
			scrapes.Add(1)
			if err != nil {
				scrapeErrors.Add(1)
			}
		}
	})

	sched := schedule.New(schedule.Config{
		Runner: schedule.LightRunner(nil),
		Sink:   store.Fanout{st, col, jw, cnt},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if _, err := sched.Update(specs(t, target)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	t.Logf("%d sessions × %d pps for %v against %v (window %v, history %d, timestamping %s, store %s)",
		*flagSessions, *flagPPS, *flagDuration, target, *flagWindow, history, *flagMode, *flagStore)

	var samples []sample
	record := func(s sample) {
		samples = append(samples, s)
		cpu := 0.0
		if n := len(samples); n > 1 {
			p := samples[n-2]
			cpu = float64(s.cpu-p.cpu) / float64(s.at-p.at) * 100
		}
		t.Logf("%8v goroutines %5d  live heap %9s  heap in use %9s  RSS %9s  fds %4d  CPU %5.1f%%  packets %9d  summaries %6d  db %9s",
			s.at, s.goroutines, mib(s.liveHeap), mib(s.heapInuse), mib(s.rss), s.fds, cpu, s.packets, s.summaries, mib(s.db))
	}
	record(take(start, cnt))
	for time.Since(start) < *flagDuration {
		time.Sleep(min(*flagSample, *flagDuration-time.Since(start)))
		record(take(start, cnt))
	}
	runtime.GC()
	end := take(start, cnt)

	// Shut down, then everything should be gone.
	sched.Close()
	stopScrape()
	srv.Close()
	watch.Close()
	stopRefl()
	if err := <-reflDone; err != nil {
		t.Errorf("reflector: %v", err)
	}
	aux.Wait()
	if err := jw.Close(); err != nil {
		t.Errorf("jsonl: %v", err)
	}
	if db != nil {
		// Close writes the queue out: every result kept is in the file.
		closing := time.Now()
		if err := db.Close(); err != nil {
			t.Errorf("sqlite: %v", err)
		}
		ds := db.Stats()
		t.Logf("sqlite: written %d, dropped %d, failed %d of %d results kept; %s on disk; closed in %v",
			ds.Written, ds.Dropped, ds.Failed, cnt.kept.Load(), mib(dbSize()), time.Since(closing).Round(time.Millisecond))
		if ds.Dropped != 0 || ds.Failed != 0 || ds.Written != cnt.kept.Load() {
			t.Errorf("sqlite kept %d of %d results (dropped %d, failed %d)", ds.Written, cnt.kept.Load(), ds.Dropped, ds.Failed)
		}
		dbFile = ""
	}
	after := settle(base)

	writeCSV(t, samples)
	report(t, base, samples, end, after, cnt)
	if n := scrapes.Load(); scrapeErrors.Load() != 0 || n == 0 {
		t.Errorf("%d of %d scrapes failed", scrapeErrors.Load(), n)
	}
	t.Logf("scrapes %d, JSON Lines written %d, dropped %d; store watcher dropped %d",
		scrapes.Load(), jw.Written(), jw.Dropped(), watch.Dropped())
	if jw.Dropped() != 0 {
		t.Errorf("JSON Lines dropped %d records", jw.Dropped())
	}
}

// settle waits up to 5 s for goroutines and descriptors to come back down
// to base, and returns the last reading.
func settle(base sample) sample {
	var s sample
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		runtime.GC()
		s = take(time.Now(), &counter{})
		if s.goroutines <= base.goroutines+2 && s.fds <= base.fds || time.Now().After(deadline) {
			return s
		}
	}
}

func report(t *testing.T, base sample, samples []sample, end, after sample, c *counter) {
	t.Helper()
	sent, recv := c.sent.Load(), c.received.Load()
	loss := 0.0
	if sent > 0 {
		loss = float64(c.lost.Load()) / float64(sent)
	}
	first, last := samples[0], samples[len(samples)-1]
	cpu := float64(last.cpu-first.cpu) / float64(last.at-first.at) * 100
	t.Logf("results: %d summaries (%d aborted), %d run errors, %d skipped; sent %d, received %d, lost %d (%.4f%%); %d packet events",
		c.summaries.Load(), c.aborted.Load(), c.errors.Load(), c.skipped.Load(), sent, recv, c.lost.Load(), loss*100, c.packets.Load())
	t.Logf("CPU %.1f%% of one core on average (%d CPUs, GOMAXPROCS %d)", cpu, runtime.NumCPU(), runtime.GOMAXPROCS(0))

	if cpu > *flagMaxCPU {
		t.Errorf("CPU %.1f%% of one core, above %.0f%%", cpu, *flagMaxCPU)
	}
	for _, s := range samples {
		if s.rss > *flagMaxRSS {
			t.Errorf("RSS %s at %v, above %s", mib(s.rss), s.at, mib(*flagMaxRSS))
			break
		}
	}
	if c.summaries.Load() == 0 || sent == 0 {
		t.Fatal("no results")
	}
	if c.errors.Load() != 0 {
		t.Errorf("%d runs failed; last: %s", c.errors.Load(), *c.lastError.Load())
	}
	if loss > *flagMaxLoss {
		t.Errorf("loss %.4f%% on loopback", loss*100)
	}

	// Steady state: from the middle of the run, once the store holds its
	// full history, nothing may keep growing.
	mid := samples[len(samples)/2]
	t.Logf("middle (%v): goroutines %d, live heap %s; end: goroutines %d, live heap %s",
		mid.at, mid.goroutines, mib(mid.liveHeap), end.goroutines, mib(end.liveHeap))
	if end.goroutines > mid.goroutines+*flagGoroutineGrowth {
		t.Errorf("goroutines grew from %d to %d", mid.goroutines, end.goroutines)
	}
	allowed := uint64(float64(mid.liveHeap)*(1+*flagHeapGrowth)) + uint64(*flagHeapSlack)
	if end.liveHeap > allowed {
		t.Errorf("live heap grew from %s to %s (allowed %s)", mib(mid.liveHeap), mib(end.liveHeap), mib(allowed))
	}

	// After shutdown: back to where it started.
	t.Logf("before start: goroutines %d, fds %d; after shutdown: goroutines %d, fds %d, live heap %s",
		base.goroutines, base.fds, after.goroutines, after.fds, mib(after.liveHeap))
	if after.goroutines > base.goroutines+2 {
		buf := make([]byte, 1<<20)
		t.Errorf("%d goroutines left after shutdown:\n%s", after.goroutines-base.goroutines, buf[:runtime.Stack(buf, true)])
	}
	if after.fds > base.fds {
		t.Errorf("%d file descriptors left after shutdown", after.fds-base.fds)
	}
}

func writeCSV(t *testing.T, samples []sample) {
	t.Helper()
	if *flagCSV == "" {
		return
	}
	f, err := os.Create(*flagCSV)
	if err != nil {
		t.Errorf("csv: %v", err)
		return
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write([]string{"seconds", "goroutines", "live_heap", "heap_inuse", "rss", "fds", "cpu_seconds", "packets", "summaries", "db"})
	for _, s := range samples {
		_ = w.Write([]string{
			strconv.Itoa(int(s.at.Seconds())), strconv.Itoa(s.goroutines),
			strconv.FormatUint(s.liveHeap, 10), strconv.FormatUint(s.heapInuse, 10), strconv.FormatUint(s.rss, 10),
			strconv.Itoa(s.fds), strconv.FormatFloat(s.cpu.Seconds(), 'f', 3, 64),
			strconv.FormatUint(s.packets, 10), strconv.FormatUint(s.summaries, 10), strconv.FormatUint(s.db, 10),
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Errorf("csv: %v", err)
	}
}
