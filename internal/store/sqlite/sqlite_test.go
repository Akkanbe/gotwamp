package sqlite_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/store/sqlite"
	"github.com/Akkanbe/gotwamp/internal/store/storetest"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestMain fails the package's tests when they leave goroutines behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// definition stands for the session definition gotwampd keeps.
type definition struct {
	Target   string
	Interval time.Duration
}

var codec = sqlite.DefinitionCodec{
	Encode: func(def any) ([]byte, error) { return json.Marshal(def) },
	Decode: func(b []byte) (any, error) {
		var d definition
		err := json.Unmarshal(b, &d)
		return d, err
	},
}

func open(t *testing.T, cfg sqlite.Config) *sqlite.Store {
	t.Helper()
	if cfg.Path == "" {
		cfg.Path = filepath.Join(t.TempDir(), "results.db")
	}
	s, err := sqlite.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

// TestContract runs the store contract on the SQLite store.
func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T, history, maxPackets int) store.Store {
		return open(t, sqlite.Config{History: history, MaxPackets: maxPackets, Retention: -1})
	})
}

// richSummary sets a field of every kind a Summary has.
func richSummary(i int) *sender.Summary {
	return &sender.Summary{
		Summary: stats.Summary{
			Sent: 100, Received: 98, Lost: 2, LossRatio: 0.02, OWDValid: true,
			RTT:           stats.Distribution{Count: 98, Samples: 98, Min: time.Millisecond, Median: 2 * time.Millisecond, P999: 3 * time.Millisecond},
			Jitter:        stats.Jitter{RFC3550: 5 * time.Microsecond},
			ForwardHops:   stats.Hops{Count: 98, Min: 1, Max: 3, Mode: 2},
			DSCPReflected: stats.DSCPReflected{Count: 98, Mode: 46},
		},
		Window:    i,
		StartedAt: storetest.T0.Add(time.Duration(i) * time.Minute),
		EndedAt:   storetest.T0.Add(time.Duration(i)*time.Minute + 10*time.Second),
		SID:       wire.SID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Invalid:   true, Malformed: 1, ForeignSource: 2,
		Local: netip.MustParseAddrPort("192.0.2.1:50000"), Remote: netip.MustParseAddrPort("[2001:db8::1]:862"),
		TxMissing: 3, ReflectedMismatch: 4,
	}
}

// sameEvent compares events through their JSON, which ignores time
// locations and monotonic readings.
func sameEvent(t *testing.T, got, want store.Event) {
	t.Helper()
	g, _ := json.Marshal(struct {
		E store.Event
		S store.SessionInfo
	}{got, *got.Session})
	w, _ := json.Marshal(struct {
		E store.Event
		S store.SessionInfo
	}{want, *want.Session})
	if !bytes.Equal(g, w) || !got.Time.Equal(want.Time) {
		t.Errorf("event\n got  %s\n want %s", g, w)
	}
}

// TestRestart checks that results survive closing and reopening: the latest
// History in memory for Recent, all of them in the database for Range, with
// every field of the summaries, the run errors and the session definitions.
func TestRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	info := &store.SessionInfo{
		Name: "alpha", Target: "192.0.2.10", Mode: "full", Labels: map[string]string{"site": "osaka"}, Count: 100,
		Interval: 10 * time.Millisecond, Padding: 27, DSCP: 46, ControlPort: 862,
		Definition: definition{Target: "192.0.2.10", Interval: 10 * time.Millisecond},
	}
	var published []store.Event
	s, err := sqlite.Open(sqlite.Config{Path: path, History: 3, Definitions: codec, Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		e := store.Event{Kind: store.EventWindowSummary, Time: storetest.T0.Add(time.Duration(i) * time.Minute), Session: info, RunID: "01RUN", SID: []byte{9, 9}, Summary: richSummary(i)}
		s.Publish(e)
		published = append(published, e)
	}
	failure := store.Event{Kind: store.EventRunError, Time: storetest.T0.Add(5 * time.Minute), Session: info, RunID: "01RUN2", Error: &store.RunError{Stage: "request", Message: "rejected", Accept: 3}}
	s.Publish(failure)
	published = append(published, failure)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Written != 6 || st.Dropped != 0 || st.Failed != 0 {
		t.Errorf("stats %+v", st)
	}

	s = open(t, sqlite.Config{Path: path, History: 3, Definitions: codec, Retention: -1})
	if got := s.Sessions(); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("Sessions = %v", got)
	}
	recent := s.Recent("alpha", 0)
	if len(recent) != 3 {
		t.Fatalf("Recent = %d events, want History 3", len(recent))
	}
	for i, e := range recent {
		sameEvent(t, e, published[3+i])
	}
	all := s.Range("alpha", storetest.T0, storetest.T0.Add(time.Hour))
	if len(all) != 6 {
		t.Fatalf("Range = %d events, want all 6", len(all))
	}
	for i, e := range all {
		sameEvent(t, e, published[i])
	}
	if all[0].Session != all[5].Session || all[0].Session.Definition != info.Definition {
		t.Errorf("descriptions not shared or definition lost: %+v", all[0].Session)
	}
}

func TestRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	now := storetest.T0.Add(48 * time.Hour)
	s, err := sqlite.Open(sqlite.Config{Path: path, Retention: 24 * time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	s.Publish(storetest.SummaryAt(storetest.Alpha, 0))             // 48 h old
	s.Publish(storetest.SummaryAt(storetest.Alpha, 25*60))         // 23 h old
	s.Publish(storetest.SummaryAt(storetest.Beta, 0))              // 48 h old
	if got := s.Range("alpha", storetest.T0, now); len(got) != 2 { // not swept yet
		t.Errorf("before the sweep: %d", len(got))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Open sweeps: beta has no result left.
	s = open(t, sqlite.Config{Path: path, Retention: 24 * time.Hour, Now: func() time.Time { return now }})
	if got := storetest.Sents(s.Range("alpha", storetest.T0, now)); !slices.Equal(got, []int{25 * 60}) {
		t.Errorf("after the sweep: %v", got)
	}
	if got := s.Sessions(); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("Sessions = %v", got)
	}
}

func TestDeletePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	s, err := sqlite.Open(sqlite.Config{Path: path, Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	s.Publish(storetest.SummaryAt(storetest.Alpha, 1))
	s.Publish(storetest.SummaryAt(storetest.Beta, 1))
	s.Delete("alpha")
	_ = s.Close()
	s = open(t, sqlite.Config{Path: path, Retention: -1})
	if got := s.Sessions(); !slices.Equal(got, []string{"beta"}) {
		t.Errorf("Sessions after Delete and restart = %v", got)
	}
}

func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.db")
	if err := os.WriteFile(path, bytes.Repeat([]byte("not a database "), 512), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(sqlite.Config{Path: path}); !errors.Is(err, sqlite.ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	var log bytes.Buffer
	s := open(t, sqlite.Config{Path: path, ResetCorrupt: true, Logger: slog.New(slog.NewTextHandler(&log, nil))})
	s.Publish(storetest.SummaryAt(storetest.Alpha, 1))
	if got := s.Range("alpha", storetest.T0, storetest.T0.Add(time.Hour)); len(got) != 1 {
		t.Errorf("the fresh database holds %d results", len(got))
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 || !strings.Contains(log.String(), "moved aside") {
		t.Errorf("moved to %v; log %s", aside, log.String())
	}
}

func TestNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	s, err := sqlite.Open(sqlite.Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := sqlite.SetSchemaVersion(path, 99); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(sqlite.Config{Path: path, ResetCorrupt: true}); !errors.Is(err, sqlite.ErrNewerSchema) {
		t.Errorf("err = %v, want ErrNewerSchema (and the file kept)", err)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := sqlite.Open(sqlite.Config{}); err == nil {
		t.Error("opened without a path")
	}
	if _, err := sqlite.Open(sqlite.Config{Path: filepath.Join(t.TempDir(), "missing", "results.db")}); err == nil {
		t.Error("opened in a missing directory")
	}
}

func TestPublishAfterClose(t *testing.T) {
	s, err := sqlite.Open(sqlite.Config{Path: filepath.Join(t.TempDir(), "results.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(s.Close(), s.Close()); err != nil {
		t.Fatal(err)
	}
	s.Publish(storetest.SummaryAt(storetest.Alpha, 1))
	s.Delete("alpha")
	if st := s.Stats(); st.Dropped != 1 {
		t.Errorf("stats %+v", st)
	}
	if got := s.Range("alpha", storetest.T0, storetest.T0.Add(time.Hour)); len(got) != 0 {
		t.Errorf("Range after Close = %v", got)
	}
}

// TestConcurrent publishes from several goroutines while others read.
func TestConcurrent(t *testing.T) {
	s := open(t, sqlite.Config{Retention: -1})
	var wg sync.WaitGroup
	for g := range 4 {
		info := &store.SessionInfo{Name: string(rune('a' + g)), Mode: "light"}
		wg.Go(func() {
			for i := range 200 {
				s.Publish(storetest.SummaryAt(info, i))
			}
		})
		wg.Go(func() {
			for range 20 {
				_ = s.Range(info.Name, storetest.T0, storetest.T0.Add(time.Hour))
				_ = s.Recent(info.Name, 5)
			}
		})
	}
	wg.Wait()
	for g := range 4 {
		if n := len(s.Range(string(rune('a'+g)), storetest.T0, storetest.T0.Add(24*time.Hour))); n != 200 {
			t.Errorf("session %c: %d results", 'a'+g, n)
		}
	}
}

// BenchmarkPublish measures the rate the writer keeps: summaries published
// and committed per second (the target is 1000/s). Give it a count below
// the Buffer, -benchtime 20000x: Publish outruns any writer, which then
// drops.
func BenchmarkPublish(b *testing.B) {
	s, err := sqlite.Open(sqlite.Config{Path: filepath.Join(b.TempDir(), "results.db"), Retention: -1, Buffer: 1 << 16})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	info := &store.SessionInfo{Name: "bench", Mode: "light"}
	sum := richSummary(1)
	start := time.Now()
	i := 0
	for b.Loop() {
		s.Publish(store.Event{Kind: store.EventRunSummary, Time: storetest.T0.Add(time.Duration(i) * time.Second), Session: info, RunID: "r", Summary: sum})
		i++
	}
	_ = s.Range("bench", storetest.T0, storetest.T0) // waits for the writer
	b.ReportMetric(float64(i)/time.Since(start).Seconds(), "summaries/s")
	if st := s.Stats(); st.Dropped != 0 {
		b.Errorf("dropped %d", st.Dropped)
	}
}

// TestVersion1Database: a database of schema version 1, whose rows hold
// JSON, is upgraded on open; its rows read as before, and new rows are
// compressed next to them.
func TestVersion1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	s, err := sqlite.Open(sqlite.Config{Path: path, Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	// What a gotwampd of version 1 leaves: no payload_formats, JSON rows.
	old := richSummary(1)
	payload, _ := json.Marshal(old)
	info, _ := json.Marshal(map[string]any{"name": "old", "target": "192.0.2.10", "mode": "full", "interval": 10_000_000, "padding": 27, "dscp": 0})
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"DROP TABLE payload_formats", nil},
		{"UPDATE schema_version SET version = 1", nil},
		{"INSERT INTO infos (id, digest, data) VALUES (1, x'01', ?)", []any{info}},
		{
			"INSERT INTO results (session, kind, time_ns, run_id, sid, info_id, data) VALUES ('old', ?, ?, '01OLD', NULL, 1, ?)",
			[]any{int(store.EventRunSummary), storetest.T0.UnixNano(), payload},
		},
	} {
		if err := sqlite.ExecSQL(path, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}

	s = open(t, sqlite.Config{Path: path, History: 10, Retention: -1})
	got := s.Range("old", storetest.T0, storetest.T0.Add(time.Hour))
	if len(got) != 1 || got[0].RunID != "01OLD" {
		t.Fatalf("version 1 rows: %+v", got)
	}
	want, _ := json.Marshal(old)
	back, _ := json.Marshal(got[0].Summary)
	if !bytes.Equal(back, want) {
		t.Errorf("version 1 summary:\n got  %s\n want %s", back, want)
	}
	if r := s.Recent("old", 0); len(r) != 1 {
		t.Errorf("Recent of version 1 rows: %d", len(r))
	}
	s.Publish(store.Event{Kind: store.EventRunSummary, Time: storetest.T0.Add(time.Minute), Session: got[0].Session, RunID: "01NEW", Summary: richSummary(2)})
	if r := s.Range("old", storetest.T0, storetest.T0.Add(time.Hour)); len(r) != 2 || r[1].RunID != "01NEW" {
		t.Errorf("old and new rows: %+v", r)
	}
	// The database is version 2 now: a gotwampd of version 1 refuses it.
	if err := sqlite.ExecSQL(path, "SELECT 1 FROM payload_formats WHERE first_octet = 1"); err != nil {
		t.Errorf("payload_formats: %v", err)
	}
}

// realisticSummary is a summary of 100 packets with every delay statistic
// set to values that differ between runs.
func realisticSummary(i int) *sender.Summary {
	d := func(base int) stats.Distribution {
		ns := time.Duration(base + i*37)
		return stats.Distribution{
			Count: 100, Samples: 100, Min: ns, Max: 9 * ns, Mean: 2*ns + 11, Median: 2*ns - 7, StdDev: ns + 3,
			P90: 4*ns + 1, P95: 5*ns + 9, P99: 7*ns + 2, P999: 8*ns + 5,
		}
	}
	return &sender.Summary{
		Summary: stats.Summary{
			Sent: 100, Received: 99, Lost: 1, LossRatio: 0.01, RTT: d(14857), Forward: d(3191), Reverse: d(11665), ReflectorDelay: d(37053),
			Jitter:      stats.Jitter{IPDVRTT: d(7321), IPDVForward: d(2976), IPDVReverse: d(5248), PDV: d(8694), RFC3550: 7135, P95P50: 10957},
			ForwardHops: stats.Hops{Count: 99, Min: 4, Max: 4, Mode: 4}, OWDValid: true, SenderSynced: true, ReflectorSynced: true,
		},
		StartedAt: storetest.T0.Add(time.Duration(i) * 10 * time.Second), EndedAt: storetest.T0.Add(time.Duration(i)*10*time.Second + 9*time.Second),
		Local: netip.MustParseAddrPort("192.0.2.1:50123"), Remote: netip.MustParseAddrPort("192.0.2.10:862"), Timestamping: 1, TxTimestamps: true,
	}
}

// TestRowSize: a result takes at most 1 KiB of the database file, row,
// payload and indexes included (soak: 4.1 KiB as JSON).
func TestRowSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.db")
	s, err := sqlite.Open(sqlite.Config{Path: path, Retention: -1})
	if err != nil {
		t.Fatal(err)
	}
	const sessions, runs = 20, 200
	infos := make([]*store.SessionInfo, sessions)
	for i := range infos {
		infos[i] = &store.SessionInfo{Name: fmt.Sprintf("session-%03d", i), Target: "192.0.2.10", Mode: "light", Interval: 100 * time.Millisecond, Padding: 27}
	}
	for r := range runs {
		for i, info := range infos {
			s.Publish(store.Event{
				Kind: store.EventWindowSummary, Time: storetest.T0.Add(time.Duration(r) * 10 * time.Second),
				Session: info, RunID: fmt.Sprintf("01J%023d", r*sessions+i), Summary: realisticSummary(r + i),
			})
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Checkpoint the WAL into the file, as a long run does.
	if err := sqlite.ExecSQL(path, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	per := fi.Size() / (sessions * runs)
	t.Logf("%d results: %d octets, %d per result", sessions*runs, fi.Size(), per)
	if per > 1024 {
		t.Errorf("%d octets per result, want at most 1024", per)
	}
}
