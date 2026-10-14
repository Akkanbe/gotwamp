package prom

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
)

var (
	end   = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	alpha = &store.SessionInfo{Name: "alpha", Target: "192.0.2.10", Mode: "light", Labels: map[string]string{"site": "tokyo"}}
	beta  = &store.SessionInfo{Name: "beta", Target: "192.0.2.11", Mode: "full", Labels: map[string]string{"link": "primary", "bad-name": "x", "mode": "y"}}
)

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

func packet(kind stats.Kind, rtt, fwd, rev float64, valid bool) store.Event {
	return store.Event{Kind: store.EventPacket, Session: alpha, Packet: sender.PacketResult{Result: stats.Result{
		Kind: kind,
		Metrics: stats.Metrics{
			RTT: ms(rtt), Forward: ms(fwd), Reverse: ms(rev), ReflectorDelay: ms(0.5),
			DelayUsable: true, OWDValid: valid,
		},
	}}}
}

func dist(lo, mean, median, hi, p95, p99, sd float64) stats.Distribution {
	return stats.Distribution{
		Count: 2, Min: ms(lo), Mean: ms(mean), Median: ms(median), Max: ms(hi),
		P95: ms(p95), P99: ms(p99), StdDev: ms(sd),
	}
}

// scenario feeds two sessions: alpha had a run and then a failure; beta, a
// Full session, had one run with valid one-way delays.
func scenario(c *Collector) {
	c.Publish(packet(stats.KindAccepted, 5, 2, 3, true))
	c.Publish(packet(stats.KindAccepted, 20, 9, 11, false))
	c.Publish(packet(stats.KindDupReverse, 1, 1, 1, true))
	c.Publish(packet(stats.KindAccepted, -1, 1, 1, true))
	rtt := dist(5, 12.5, 12.5, 20, 19.25, 19.85, 10)
	c.Publish(store.Event{Kind: store.EventRunSummary, Time: end, Session: alpha, Summary: &sender.Summary{
		Summary: stats.Summary{
			Sent: 10, Received: 8, Lost: 2, DupReverse: 1, Reordered: 1, LossRatio: 0.2, RTT: rtt,
			Jitter: stats.Jitter{
				IPDVRTT: stats.Distribution{Count: 1, Mean: ms(15), Max: ms(15)},
				PDV:     stats.Distribution{Count: 2, P95: ms(14.25), P99: ms(14.85)},
				RFC3550: ms(1), P95P50: ms(6.75),
			},
			ForwardHops: stats.Hops{Count: 2, Mode: 4},
		},
		EndedAt: end,
	}})
	c.Publish(store.Event{Kind: store.EventRunSkipped, Session: alpha})
	c.Publish(store.Event{Kind: store.EventRunError, Time: end.Add(time.Minute), Session: alpha, Error: &store.RunError{Stage: "connect"}})
	c.Publish(store.Event{Kind: store.EventRunSummary, Session: alpha, Summary: &sender.Summary{
		Summary: stats.Summary{Sent: 3, Pending: 3}, Aborted: true,
	}})

	fwd := stats.Distribution{Count: 1, Min: ms(4), Mean: ms(4), Median: ms(4), Max: ms(4), P95: ms(4), P99: ms(4)}
	c.Publish(store.Event{Kind: store.EventWindowSummary, Time: end, Session: beta, Summary: &sender.Summary{
		Summary: stats.Summary{Sent: 5, Received: 5, OWDValid: true, Forward: fwd, Reverse: fwd},
		EndedAt: end,
	}})
	c.ObserveControlSetup(beta, ms(5))

	// Ad hoc measurements are not exported.
	adhoc := &store.SessionInfo{Target: "192.0.2.12"}
	c.Publish(store.Event{Kind: store.EventRunSummary, Session: adhoc, Summary: &sender.Summary{Summary: stats.Summary{Sent: 1}}})
	c.ObserveControlSetup(adhoc, time.Second)
}

const (
	a = `link="",mode="light",session="alpha",site="tokyo",target="192.0.2.10"`
	b = `link="primary",mode="full",session="beta",site="",target="192.0.2.11"`
)

// sortedLabels inserts extra into the label set base, keeping the names sorted as the
// exposition format writes them.
func sortedLabels(base, extra string) string {
	pairs := append(strings.Split(base, ","), extra)
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

func TestSessionMetrics(t *testing.T) {
	c := New(Config{Buckets: []time.Duration{10 * time.Millisecond, time.Millisecond}})
	scenario(c)
	g := func(v float64) string { return fmt.Sprint(v) }
	tests := map[string]string{
		"gotwamp_sender_packets_sent_total": `
# HELP gotwamp_sender_packets_sent_total Test packets sent.
# TYPE gotwamp_sender_packets_sent_total counter
gotwamp_sender_packets_sent_total{` + a + `} 13
gotwamp_sender_packets_sent_total{` + b + `} 5
`,
		"gotwamp_sender_packets_duplicate_total": `
# HELP gotwamp_sender_packets_duplicate_total Duplicate replies, by the path that duplicated them.
# TYPE gotwamp_sender_packets_duplicate_total counter
gotwamp_sender_packets_duplicate_total{` + sortedLabels(a, `direction="forward"`) + `} 0
gotwamp_sender_packets_duplicate_total{` + sortedLabels(a, `direction="reverse"`) + `} 1
gotwamp_sender_packets_duplicate_total{` + sortedLabels(b, `direction="forward"`) + `} 0
gotwamp_sender_packets_duplicate_total{` + sortedLabels(b, `direction="reverse"`) + `} 0
`,
		// The duplicate and the negative RTT are not observed.
		"gotwamp_sender_rtt_seconds": `
# HELP gotwamp_sender_rtt_seconds Round-trip time excluding the reflector's processing time.
# TYPE gotwamp_sender_rtt_seconds histogram
gotwamp_sender_rtt_seconds_bucket{` + a + `,le="0.001"} 0
gotwamp_sender_rtt_seconds_bucket{` + a + `,le="0.01"} 1
gotwamp_sender_rtt_seconds_bucket{` + a + `,le="+Inf"} 2
gotwamp_sender_rtt_seconds_sum{` + a + `} ` + g(ms(5).Seconds()+ms(20).Seconds()) + `
gotwamp_sender_rtt_seconds_count{` + a + `} 2
gotwamp_sender_rtt_seconds_bucket{` + b + `,le="0.001"} 0
gotwamp_sender_rtt_seconds_bucket{` + b + `,le="0.01"} 0
gotwamp_sender_rtt_seconds_bucket{` + b + `,le="+Inf"} 0
gotwamp_sender_rtt_seconds_sum{` + b + `} 0
gotwamp_sender_rtt_seconds_count{` + b + `} 0
`,
		// Only the packet with valid one-way delays.
		"gotwamp_sender_forward_delay_seconds": `
# HELP gotwamp_sender_forward_delay_seconds Forward one-way delay, of packets with valid one-way delays.
# TYPE gotwamp_sender_forward_delay_seconds histogram
gotwamp_sender_forward_delay_seconds_bucket{` + a + `,le="0.001"} 0
gotwamp_sender_forward_delay_seconds_bucket{` + a + `,le="0.01"} 1
gotwamp_sender_forward_delay_seconds_bucket{` + a + `,le="+Inf"} 1
gotwamp_sender_forward_delay_seconds_sum{` + a + `} 0.002
gotwamp_sender_forward_delay_seconds_count{` + a + `} 1
gotwamp_sender_forward_delay_seconds_bucket{` + b + `,le="0.001"} 0
gotwamp_sender_forward_delay_seconds_bucket{` + b + `,le="0.01"} 0
gotwamp_sender_forward_delay_seconds_bucket{` + b + `,le="+Inf"} 0
gotwamp_sender_forward_delay_seconds_sum{` + b + `} 0
gotwamp_sender_forward_delay_seconds_count{` + b + `} 0
`,
		// Only Full sessions have a control setup.
		"gotwamp_sender_control_setup_seconds": `
# HELP gotwamp_sender_control_setup_seconds TWAMP-Control setup time, from connect to Start-Ack.
# TYPE gotwamp_sender_control_setup_seconds histogram
gotwamp_sender_control_setup_seconds_bucket{` + b + `,le="0.001"} 0
gotwamp_sender_control_setup_seconds_bucket{` + b + `,le="0.01"} 1
gotwamp_sender_control_setup_seconds_bucket{` + b + `,le="+Inf"} 1
gotwamp_sender_control_setup_seconds_sum{` + b + `} 0.005
gotwamp_sender_control_setup_seconds_count{` + b + `} 1
`,
		"gotwamp_sender_last_run_rtt_seconds": `
# HELP gotwamp_sender_last_run_rtt_seconds RTT statistics of the latest run or window.
# TYPE gotwamp_sender_last_run_rtt_seconds gauge
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="min"`) + `} 0.005
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="mean"`) + `} 0.0125
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="median"`) + `} 0.0125
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="max"`) + `} 0.02
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="p95"`) + `} 0.01925
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="p99"`) + `} 0.01985
gotwamp_sender_last_run_rtt_seconds{` + sortedLabels(a, `stat="stddev"`) + `} 0.01
`,
		// Exported only when valid: beta's, not alpha's.
		"gotwamp_sender_last_run_forward_delay_seconds": `
# HELP gotwamp_sender_last_run_forward_delay_seconds Forward delay statistics of the latest run or window, when valid.
# TYPE gotwamp_sender_last_run_forward_delay_seconds gauge
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="min"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="mean"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="median"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="max"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="p95"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="p99"`) + `} 0.004
gotwamp_sender_last_run_forward_delay_seconds{` + sortedLabels(b, `stat="stddev"`) + `} 0
`,
		"gotwamp_sender_last_run_jitter_seconds": `
# HELP gotwamp_sender_last_run_jitter_seconds Delay variation of the latest run or window.
# TYPE gotwamp_sender_last_run_jitter_seconds gauge
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="ipdv_mean"`) + `} 0.015
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="ipdv_max"`) + `} 0.015
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="pdv_p95"`) + `} 0.01425
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="pdv_p99"`) + `} 0.01485
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="rfc3550"`) + `} 0.001
gotwamp_sender_last_run_jitter_seconds{` + sortedLabels(a, `kind="p95_p50"`) + `} 0.00675
`,
		// alpha's last run failed after its last summary.
		"gotwamp_sender_last_run_success gotwamp_sender_last_run_timestamp_seconds gotwamp_sender_last_run_loss_ratio gotwamp_sender_last_run_owd_valid": `
# HELP gotwamp_sender_last_run_success 1 when the latest run completed, 0 when it failed.
# TYPE gotwamp_sender_last_run_success gauge
gotwamp_sender_last_run_success{` + a + `} 0
gotwamp_sender_last_run_success{` + b + `} 1
# HELP gotwamp_sender_last_run_timestamp_seconds End time of the latest run or window, as a Unix time.
# TYPE gotwamp_sender_last_run_timestamp_seconds gauge
gotwamp_sender_last_run_timestamp_seconds{` + a + `} ` + g(float64(end.Add(time.Minute).Unix())) + `
gotwamp_sender_last_run_timestamp_seconds{` + b + `} ` + g(float64(end.Unix())) + `
# HELP gotwamp_sender_last_run_loss_ratio Loss ratio of the latest run or window.
# TYPE gotwamp_sender_last_run_loss_ratio gauge
gotwamp_sender_last_run_loss_ratio{` + a + `} 0.2
gotwamp_sender_last_run_loss_ratio{` + b + `} 0
# HELP gotwamp_sender_last_run_owd_valid 1 when the one-way delays of the latest run are valid.
# TYPE gotwamp_sender_last_run_owd_valid gauge
gotwamp_sender_last_run_owd_valid{` + a + `} 0
gotwamp_sender_last_run_owd_valid{` + b + `} 1
`,
		"gotwamp_sender_path_hops gotwamp_sender_runs_total gotwamp_sender_run_errors_total": `
# HELP gotwamp_sender_path_hops Most frequent hop count of the latest run.
# TYPE gotwamp_sender_path_hops gauge
gotwamp_sender_path_hops{` + sortedLabels(a, `direction="forward"`) + `} 4
# HELP gotwamp_sender_runs_total Runs and windows, by result.
# TYPE gotwamp_sender_runs_total counter
gotwamp_sender_runs_total{` + sortedLabels(a, `result="success"`) + `} 1
gotwamp_sender_runs_total{` + sortedLabels(a, `result="error"`) + `} 1
gotwamp_sender_runs_total{` + sortedLabels(a, `result="skipped"`) + `} 1
gotwamp_sender_runs_total{` + sortedLabels(b, `result="success"`) + `} 1
gotwamp_sender_runs_total{` + sortedLabels(b, `result="error"`) + `} 0
gotwamp_sender_runs_total{` + sortedLabels(b, `result="skipped"`) + `} 0
# HELP gotwamp_sender_run_errors_total Failed runs, by the stage that failed.
# TYPE gotwamp_sender_run_errors_total counter
gotwamp_sender_run_errors_total{` + sortedLabels(a, `stage="connect"`) + `} 1
`,
	}
	for names, want := range tests {
		t.Run(strings.Fields(names)[0], func(t *testing.T) {
			if err := testutil.CollectAndCompare(c, strings.NewReader(want), strings.Fields(names)...); err != nil {
				t.Error(err)
			}
		})
	}

	if n := testutil.CollectAndCount(c, "gotwamp_sender_packets_received_total"); n != 2 {
		t.Errorf("%d received series", n)
	}
	c.Forget("alpha")
	if n := testutil.CollectAndCount(c, "gotwamp_sender_packets_received_total"); n != 1 {
		t.Errorf("%d received series after Forget", n)
	}
}

func TestLint(t *testing.T) {
	c := New(Config{
		Clock:        clockFunc(func() (bool, time.Duration) { return true, time.Millisecond }),
		Reflector:    ReflectorSourceFunc(snapshot),
		Timestamping: "kernel",
		JSONLDropped: func() uint64 { return 0 },
	})
	scenario(c)
	problems, err := testutil.CollectAndLint(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("%s: %s", p.Metric, p.Text)
	}
}

type clockFunc func() (bool, time.Duration)

func (f clockFunc) Status() (bool, time.Duration) { return f() }

func snapshot() ReflectorSnapshot {
	return ReflectorSnapshot{
		Listeners: []ReflectorListener{
			{Kind: "light", Listener: "0.0.0.0:862", Received: 10, Reflected: 9},
			{Kind: "full", Listener: "[::]:20000", Received: 4, Reflected: 4},
		},
		Dropped:                  []ReflectorDrops{{Kind: "light", Reason: "acl", Count: 1}},
		ControlConnectionsActive: 2,
		ControlConnections:       map[string]uint64{"accepted": 5, "rejected": 1},
		SessionsActive:           map[string]int{"full": 1, "light": 3},
		LightFlowsOverflow:       7,
	}
}

func TestDaemonMetrics(t *testing.T) {
	c := New(Config{
		Clock:        clockFunc(func() (bool, time.Duration) { return true, 250 * time.Microsecond }),
		Reflector:    ReflectorSourceFunc(snapshot),
		Timestamping: "userspace",
		Version:      "v0.1.0",
		Commit:       "abc1234",
		JSONLDropped: func() uint64 { return 3 },
	})
	want := `
# HELP gotwamp_build_info Build information.
# TYPE gotwamp_build_info gauge
gotwamp_build_info{commit="abc1234",go_version="` + runtime.Version() + `",version="v0.1.0"} 1
# HELP gotwamp_clock_max_error_seconds Maximum error of the local clock.
# TYPE gotwamp_clock_max_error_seconds gauge
gotwamp_clock_max_error_seconds 0.00025
# HELP gotwamp_clock_synchronized 1 when the local clock is synchronized.
# TYPE gotwamp_clock_synchronized gauge
gotwamp_clock_synchronized 1
# HELP gotwamp_jsonl_dropped_total JSON Lines records dropped because the writer fell behind.
# TYPE gotwamp_jsonl_dropped_total counter
gotwamp_jsonl_dropped_total 3
# HELP gotwamp_reflector_control_connections_active Open TWAMP-Control connections.
# TYPE gotwamp_reflector_control_connections_active gauge
gotwamp_reflector_control_connections_active 2
# HELP gotwamp_reflector_control_connections_total TWAMP-Control connections, by result.
# TYPE gotwamp_reflector_control_connections_total counter
gotwamp_reflector_control_connections_total{result="accepted"} 5
gotwamp_reflector_control_connections_total{result="rejected"} 1
# HELP gotwamp_reflector_light_flows_overflow_total Packets of new Light flows handled statelessly because the flow table was full.
# TYPE gotwamp_reflector_light_flows_overflow_total counter
gotwamp_reflector_light_flows_overflow_total 7
# HELP gotwamp_reflector_packets_dropped_total Test packets dropped by the reflector, by reason.
# TYPE gotwamp_reflector_packets_dropped_total counter
gotwamp_reflector_packets_dropped_total{kind="light",reason="acl"} 1
# HELP gotwamp_reflector_packets_received_total Test packets received by the reflector.
# TYPE gotwamp_reflector_packets_received_total counter
gotwamp_reflector_packets_received_total{kind="full",listener="[::]:20000"} 4
gotwamp_reflector_packets_received_total{kind="light",listener="0.0.0.0:862"} 10
# HELP gotwamp_reflector_packets_reflected_total Test packets reflected.
# TYPE gotwamp_reflector_packets_reflected_total counter
gotwamp_reflector_packets_reflected_total{kind="full",listener="[::]:20000"} 4
gotwamp_reflector_packets_reflected_total{kind="light",listener="0.0.0.0:862"} 9
# HELP gotwamp_reflector_sessions_active Full sessions and Light flows with state.
# TYPE gotwamp_reflector_sessions_active gauge
gotwamp_reflector_sessions_active{kind="full"} 1
gotwamp_reflector_sessions_active{kind="light"} 3
# HELP gotwamp_timestamping_mode The timestamping mode in effect.
# TYPE gotwamp_timestamping_mode gauge
gotwamp_timestamping_mode{mode="userspace"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Error(err)
	}

	// Without sources only the build information remains.
	if n := testutil.CollectAndCount(New(Config{})); n != 1 {
		t.Errorf("%d metrics without sources", n)
	}
}

func TestHandler(t *testing.T) {
	var health error
	c := New(Config{Health: func() error { return health }})
	scenario(c)
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	get := func(path string) (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	code, body := get("/metrics")
	if code != http.StatusOK || !strings.Contains(body, "gotwamp_sender_packets_sent_total{") ||
		!strings.Contains(body, "go_goroutines") {
		t.Errorf("/metrics: %d\n%s", code, body)
	}
	if code, body := get("/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/healthz: %d %q", code, body)
	}
	health = errors.New("api down")
	if code, body := get("/healthz"); code != http.StatusServiceUnavailable || !strings.Contains(body, "api down") {
		t.Errorf("/healthz: %d %q", code, body)
	}
	if code, _ := get("/nope"); code != http.StatusNotFound {
		t.Errorf("/nope: %d", code)
	}
}

// loaded returns a Collector with n sessions, each with a summary and some
// packets behind it.
func loaded(n int) *Collector {
	c := New(Config{})
	for i := range n {
		info := &store.SessionInfo{Name: fmt.Sprintf("s%03d", i), Target: "192.0.2.10", Mode: "light", Labels: map[string]string{"site": "x"}}
		for range 10 {
			e := packet(stats.KindAccepted, 5, 2, 3, true)
			e.Session = info
			c.Publish(e)
		}
		rtt := dist(5, 12.5, 12.5, 20, 19.25, 19.85, 10)
		c.Publish(store.Event{Kind: store.EventWindowSummary, Time: end, Session: info, Summary: &sender.Summary{
			Summary: stats.Summary{Sent: 10, Received: 10, RTT: rtt, OWDValid: true, Forward: rtt, Reverse: rtt}, EndedAt: end,
		}})
	}
	return c
}

// BenchmarkCollect is one scrape of 100 sessions.
func BenchmarkCollect(b *testing.B) {
	c := loaded(100)
	ch := make(chan prometheus.Metric, 1<<16)
	b.ReportAllocs()
	for b.Loop() {
		c.Collect(ch)
		for len(ch) > 0 {
			<-ch
		}
	}
}

// BenchmarkPublishPacket is the per-packet work of the Collector.
func BenchmarkPublishPacket(b *testing.B) {
	c := loaded(100)
	e := packet(stats.KindAccepted, 5, 2, 3, true)
	b.ReportAllocs()
	for b.Loop() {
		c.Publish(e)
	}
}

// TestAnomalyCounters: the per-packet anomalies are counted, and an invalid
// summary is no success.
func TestAnomalyCounters(t *testing.T) {
	c := New(Config{})
	info := &store.SessionInfo{Name: "x", Target: "192.0.2.1", Mode: "full"}
	c.Publish(store.Event{Kind: store.EventRunSummary, Time: end, Session: info, Summary: &sender.Summary{
		Summary:   stats.Summary{Sent: 5, InvalidRTT: 1, NegativeOWD: 2, Unsupported: 4, Stray: 1, Late: 2},
		TxMissing: 3, TxInvalid: 7, Invalid: true, EndedAt: end, Malformed: 3, ReflectedMismatch: 8,
	}})
	lv := `mode="full",session="x",target="192.0.2.1"`
	want := `
# HELP gotwamp_sender_invalid_rtt_total Replies with a negative RTT, left out of the statistics (a clock step).
# TYPE gotwamp_sender_invalid_rtt_total counter
gotwamp_sender_invalid_rtt_total{` + lv + `} 1
# HELP gotwamp_sender_negative_owd_total Replies with a negative one-way delay, a sign of unsynchronized clocks.
# TYPE gotwamp_sender_negative_owd_total counter
gotwamp_sender_negative_owd_total{` + lv + `} 2
# HELP gotwamp_sender_tx_timestamp_missing_total Replies whose kernel transmit timestamp did not arrive in time.
# TYPE gotwamp_sender_tx_timestamp_missing_total counter
gotwamp_sender_tx_timestamp_missing_total{` + lv + `} 3
# HELP gotwamp_sender_tx_timestamp_invalid_total Replies whose kernel transmit timestamp was rejected as inconsistent, so that the user-space T1 was used.
# TYPE gotwamp_sender_tx_timestamp_invalid_total counter
gotwamp_sender_tx_timestamp_invalid_total{` + lv + `} 7
# HELP gotwamp_sender_unsupported_timestamp_format_total Replies with timestamps in an unsupported format (PTPv2), left out of the delays.
# TYPE gotwamp_sender_unsupported_timestamp_format_total counter
gotwamp_sender_unsupported_timestamp_format_total{` + lv + `} 4
# HELP gotwamp_sender_packets_stray_total Replies discarded as too short, for no packet sent, or after the timeout.
# TYPE gotwamp_sender_packets_stray_total counter
gotwamp_sender_packets_stray_total{` + lv + `} 6
# HELP gotwamp_sender_reflected_mismatch_total Replies that did not return the padding asked to be reflected (RFC 6038 Reflect Octets).
# TYPE gotwamp_sender_reflected_mismatch_total counter
gotwamp_sender_reflected_mismatch_total{` + lv + `} 8
# HELP gotwamp_sender_runs_total Runs and windows, by result.
# TYPE gotwamp_sender_runs_total counter
gotwamp_sender_runs_total{` + sortedLabels(lv, `result="error"`) + `} 0
gotwamp_sender_runs_total{` + sortedLabels(lv, `result="skipped"`) + `} 0
gotwamp_sender_runs_total{` + sortedLabels(lv, `result="success"`) + `} 0
`
	names := []string{
		"gotwamp_sender_invalid_rtt_total", "gotwamp_sender_negative_owd_total", "gotwamp_sender_tx_timestamp_missing_total",
		"gotwamp_sender_tx_timestamp_invalid_total", "gotwamp_sender_unsupported_timestamp_format_total", "gotwamp_sender_runs_total", "gotwamp_sender_packets_stray_total",
		"gotwamp_sender_reflected_mismatch_total",
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), names...); err != nil {
		t.Error(err)
	}
}

// TestPublishDuringScrape: a scrape stalled on a slow consumer does not hold
// up Publish, which runs on the receive path of every session (review M1).
func TestPublishDuringScrape(t *testing.T) {
	c := loaded(10)
	ch := make(chan prometheus.Metric) // unbuffered: Collect stalls on every send
	done := make(chan struct{})
	go func() {
		c.Collect(ch)
		close(done)
	}()
	<-ch // Collect is mid-scrape now
	published := make(chan struct{})
	go func() {
		c.Publish(packet(stats.KindAccepted, 5, 2, 3, true))
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Error("Publish waited for the scrape")
	}
	for {
		select {
		case <-ch:
		case <-done:
			<-published
			return
		}
	}
}

// BenchmarkSnapshot is how long a scrape of 100 sessions holds the lock
// that Publish takes.
func BenchmarkSnapshot(b *testing.B) {
	c := loaded(100)
	b.ReportAllocs()
	for b.Loop() {
		_ = c.snapshot()
	}
}

// TestDSCPMetrics: the DSCP and ECN the reflector saw (RFC 7750).
func TestDSCPMetrics(t *testing.T) {
	c := New(Config{})
	info := &store.SessionInfo{Name: "x", Target: "192.0.2.1", Mode: "light"}
	for range 2 {
		c.Publish(store.Event{Kind: store.EventRunSummary, Time: end, Session: info, Summary: &sender.Summary{
			Summary: stats.Summary{Sent: 5, DSCPReflected: stats.DSCPReflected{Count: 5, Mode: 10, Remarked: 5, CE: 1}},
			EndedAt: end,
		}})
	}
	lv := `mode="light",session="x",target="192.0.2.1"`
	want := `
# HELP gotwamp_sender_dscp_remarked_total Test packets whose DSCP the forward path changed (RFC 7750).
# TYPE gotwamp_sender_dscp_remarked_total counter
gotwamp_sender_dscp_remarked_total{` + lv + `} 10
# HELP gotwamp_sender_ecn_ce_total Test packets marked Congestion Experienced on the forward path (RFC 7750).
# TYPE gotwamp_sender_ecn_ce_total counter
gotwamp_sender_ecn_ce_total{` + lv + `} 2
# HELP gotwamp_sender_last_run_dscp_reflected Most frequent DSCP the reflector saw in the latest run or window (RFC 7750).
# TYPE gotwamp_sender_last_run_dscp_reflected gauge
gotwamp_sender_last_run_dscp_reflected{` + lv + `} 10
`
	names := []string{"gotwamp_sender_dscp_remarked_total", "gotwamp_sender_ecn_ce_total", "gotwamp_sender_last_run_dscp_reflected"}
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), names...); err != nil {
		t.Error(err)
	}
	// Without RFC 7750 there is no reflected DSCP to show.
	if n := testutil.CollectAndCount(New(Config{}), "gotwamp_sender_last_run_dscp_reflected"); n != 0 {
		t.Errorf("%d series without data", n)
	}
}

// TestStoreMetrics: the counters of a database result store.
func TestStoreMetrics(t *testing.T) {
	c := New(Config{Store: func() StoreStats { return StoreStats{Written: 5, Dropped: 1, Failed: 2} }})
	want := `
# HELP gotwamp_store_results_dropped_total Results not kept because the database writer was behind.
# TYPE gotwamp_store_results_dropped_total counter
gotwamp_store_results_dropped_total 1
# HELP gotwamp_store_results_failed_total Results the result store's database refused.
# TYPE gotwamp_store_results_failed_total counter
gotwamp_store_results_failed_total 2
# HELP gotwamp_store_results_written_total Results committed to the result store's database.
# TYPE gotwamp_store_results_written_total counter
gotwamp_store_results_written_total 5
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want),
		"gotwamp_store_results_written_total", "gotwamp_store_results_dropped_total", "gotwamp_store_results_failed_total"); err != nil {
		t.Error(err)
	}
}
