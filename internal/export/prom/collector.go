// Package prom exports the results and the state of gotwampd as Prometheus
// metrics (SPEC §9.6), and serves /metrics and /healthz.
//
// The Collector is a store.ResultSink: it keeps per-session counters,
// histograms and the latest summary, and builds the metrics at scrape time.
// Sessions carry user labels of their own, so the label set of every metric
// is the session, target and mode labels plus the union of the user label
// names, empty where a session lacks one.
package prom

import (
	"maps"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// defaultBuckets are the delay histogram buckets of SPEC §9.2 rtt_buckets.
var defaultBuckets = []time.Duration{
	50 * time.Microsecond, 100 * time.Microsecond, 200 * time.Microsecond, 500 * time.Microsecond,
	time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
	20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
	500 * time.Millisecond, time.Second,
}

// StoreStats are the counters of a database result store.
type StoreStats struct {
	Written, Dropped, Failed uint64
}

// Config parameterizes a Collector. Every source is optional; the metrics
// of a missing one are not exported.
type Config struct {
	// Buckets are the upper bounds of the delay histograms. Nil means
	// the rtt_buckets of SPEC §9.2.
	Buckets []time.Duration
	// Clock reports the clock state.
	Clock ClockSource
	// Reflector reports the reflector counters.
	Reflector ReflectorSource
	// Timestamping is the timestamping mode in effect: hardware, kernel or
	// userspace (tstamp.Mode.String).
	Timestamping string
	// Version and Commit identify the build in gotwamp_build_info.
	Version, Commit string
	// JSONLDropped reports the JSON Lines records dropped.
	JSONLDropped func() uint64
	// Store reports the counters of a database result store; nil without
	// one.
	Store func() StoreStats
	// Health decides /healthz: nil or a nil error is 200.
	Health func() error
}

// Collector is the Prometheus collector of gotwampd. It implements
// prometheus.Collector and store.ResultSink, and is safe for concurrent use.
type Collector struct {
	cfg     Config
	buckets []float64

	mu       sync.Mutex
	sessions map[string]*sessionMetrics
}

// sessionMetrics holds what one session contributed.
type sessionMetrics struct {
	info *store.SessionInfo

	sent, received, lost   uint64
	dupForward, dupReverse uint64
	reordered              uint64
	// The per-packet anomalies of SPEC §7.4, §7.6, §8.1 and §8.2.
	invalidRTT, negativeOWD, txMissing, txInvalid uint64
	unsupported                                   uint64
	stray                                         uint64
	// reflectedMismatch counts the replies that did not return the padding
	// asked to be reflected (RFC 6038).
	reflectedMismatch            uint64
	dscpRemarked, ecnCE          uint64
	rtt, forward, reverse, refl  histogram
	controlSetup                 histogram
	last                         *sender.Summary
	lastSuccess, lastSuccessSet  bool
	lastTime                     time.Time
	runsOK, runsFailed, runsSkip uint64
	runErrors                    map[string]uint64
}

type histogram struct {
	counts []uint64 // per bucket, not cumulative; the last one is +Inf
	count  uint64
	sum    float64
}

// clone returns an independent copy of h.
func (h histogram) clone() histogram {
	h.counts = slices.Clone(h.counts)
	return h
}

// snapshot copies the state of every session, sorted by name. The session
// info and the last summary are never modified once published, so the
// copies share them.
func (c *Collector) snapshot() []*sessionMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*sessionMetrics, 0, len(c.sessions))
	for _, m := range c.sessions {
		cp := *m
		cp.rtt, cp.forward, cp.reverse = m.rtt.clone(), m.forward.clone(), m.reverse.clone()
		cp.refl, cp.controlSetup = m.refl.clone(), m.controlSetup.clone()
		cp.runErrors = maps.Clone(m.runErrors)
		out = append(out, &cp)
	}
	slices.SortFunc(out, func(a, b *sessionMetrics) int { return strings.Compare(a.info.Name, b.info.Name) })
	return out
}

func (h *histogram) observe(bounds []float64, v float64) {
	if h.counts == nil {
		h.counts = make([]uint64, len(bounds)+1)
	}
	h.counts[sort.SearchFloat64s(bounds, v)]++
	h.count++
	h.sum += v
}

func (h *histogram) metric(desc *prometheus.Desc, bounds []float64, labels []string) prometheus.Metric {
	cum := make(map[float64]uint64, len(bounds))
	var n uint64
	for i, ub := range bounds {
		if h.counts != nil {
			n += h.counts[i]
		}
		cum[ub] = n
	}
	return prometheus.MustNewConstHistogram(desc, h.count, h.sum, cum, labels...)
}

// New returns a Collector with no sessions.
func New(cfg Config) *Collector {
	if cfg.Buckets == nil {
		cfg.Buckets = defaultBuckets
	}
	b := make([]float64, len(cfg.Buckets))
	for i, d := range cfg.Buckets {
		b[i] = d.Seconds()
	}
	slices.Sort(b)
	return &Collector{cfg: cfg, buckets: slices.Compact(b), sessions: make(map[string]*sessionMetrics)}
}

func (c *Collector) session(info *store.SessionInfo) *sessionMetrics {
	m := c.sessions[info.Name]
	if m == nil {
		m = &sessionMetrics{runErrors: map[string]uint64{}}
		c.sessions[info.Name] = m
	}
	m.info = info
	return m
}

// Publish implements store.ResultSink. Ad hoc measurements (empty session
// name) are not exported.
func (c *Collector) Publish(e store.Event) {
	if e.Session.Name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.session(e.Session)
	switch e.Kind {
	case store.EventPacket:
		m.observe(c.buckets, e.Packet)
	case store.EventRunSummary, store.EventWindowSummary:
		s := e.Summary
		m.sent += s.Sent
		m.received += s.Received
		m.lost += s.Lost
		m.dupForward += s.DupForward
		m.dupReverse += s.DupReverse
		m.reordered += s.Reordered
		m.invalidRTT += s.InvalidRTT
		m.negativeOWD += s.NegativeOWD
		m.txMissing += s.TxMissing
		m.txInvalid += s.TxInvalid
		m.reflectedMismatch += s.ReflectedMismatch
		m.unsupported += s.Unsupported
		// SPEC §6.5: replies too short, for a packet never sent, or after
		// the timeout.
		m.stray += s.Stray + s.Late + s.Malformed
		m.dscpRemarked += s.DSCPReflected.Remarked
		m.ecnCE += s.DSCPReflected.CE
		// A run cut short, or one whose results may be wrong, is no success.
		if !s.Aborted && !s.Invalid {
			m.last = s
			m.lastSuccess, m.lastSuccessSet, m.lastTime = true, true, s.EndedAt
			m.runsOK++
		}
	case store.EventRunError:
		m.lastSuccess, m.lastSuccessSet, m.lastTime = false, true, e.Time
		m.runsFailed++
		m.runErrors[e.Error.Stage]++
	case store.EventRunSkipped:
		m.runsSkip++
	}
}

// observe records one reply in the histograms: only first replies with
// usable timestamps, and one-way delays only when valid (SPEC §7.4).
func (m *sessionMetrics) observe(bounds []float64, p sender.PacketResult) {
	r := p.Result
	if r.Kind != stats.KindAccepted || !r.Metrics.DelayUsable || r.Metrics.RTT < 0 {
		return
	}
	m.rtt.observe(bounds, r.Metrics.RTT.Seconds())
	m.refl.observe(bounds, r.Metrics.ReflectorDelay.Seconds())
	if r.Metrics.OWDValid {
		m.forward.observe(bounds, r.Metrics.Forward.Seconds())
		m.reverse.observe(bounds, r.Metrics.Reverse.Seconds())
	}
}

// ObserveControlSetup records the time from connect to Start-Ack of a Full
// run of session.
func (c *Collector) ObserveControlSetup(session *store.SessionInfo, d time.Duration) {
	if session.Name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session(session).controlSetup.observe(c.buckets, d.Seconds())
}

// Forget drops the metrics of a deleted session.
func (c *Collector) Forget(session string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, session)
}

// Describe implements prometheus.Collector. It describes nothing: the
// label names depend on the sessions, so the Collector is unchecked.
func (c *Collector) Describe(chan<- *prometheus.Desc) {}

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reserved are the label names the Collector uses itself.
var reserved = map[string]bool{
	"session": true, "target": true, "mode": true, "direction": true, "stat": true,
	"kind": true, "result": true, "stage": true, "le": true,
}

// emitter builds the metrics of one scrape.
type emitter struct {
	ch   chan<- prometheus.Metric
	base []string
}

func (e emitter) desc(name, help string, extra ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, append(slices.Clone(e.base), extra...), nil)
}

func (e emitter) value(d *prometheus.Desc, t prometheus.ValueType, v float64, labels ...string) {
	e.ch <- prometheus.MustNewConstMetric(d, t, v, labels...)
}

// Collect implements prometheus.Collector. It holds the lock only to copy
// the per-session state: Publish runs on the receive path of every session
// and must not wait for a scrape (SPEC §4.4).
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	sessions := c.snapshot()

	// User labels the Collector cannot export are skipped; the
	// configuration rejects them before they get here.
	keySet := map[string]bool{}
	for _, m := range sessions {
		for k := range m.info.Labels {
			if labelName.MatchString(k) && !reserved[k] && !strings.HasPrefix(k, "__") {
				keySet[k] = true
			}
		}
	}
	keys := sortedKeys(keySet)
	e := emitter{ch: ch, base: append([]string{"session", "target", "mode"}, keys...)}
	d := newDescs(e)

	for _, m := range sessions {
		lv := []string{m.info.Name, m.info.Target, m.info.Mode}
		for _, k := range keys {
			lv = append(lv, m.info.Labels[k])
		}
		c.collectSession(e, d, m, lv)
	}
	c.collectDaemon(e)
}

// descs are the per-session metric descriptors of one scrape.
type descs struct {
	sent, received, lost, dup, reordered   *prometheus.Desc
	invalidRTT, negativeOWD                *prometheus.Desc
	txMissing, txInvalid, unsupported      *prometheus.Desc
	stray, reflectedMismatch               *prometheus.Desc
	dscpRemarked, ecnCE, lastDSCP          *prometheus.Desc
	rtt, forward, reverse, refl, setup     *prometheus.Desc
	lastRTT, lastForward, lastReverse      *prometheus.Desc
	lastJitter, lastLoss, lastTime, lastOK *prometheus.Desc
	lastOWDValid, hops, runs, runErrors    *prometheus.Desc
}

func newDescs(e emitter) descs {
	return descs{
		sent:              e.desc("gotwamp_sender_packets_sent_total", "Test packets sent."),
		received:          e.desc("gotwamp_sender_packets_received_total", "Distinct test packets reflected back."),
		lost:              e.desc("gotwamp_sender_packets_lost_total", "Test packets with no reply within the timeout."),
		dup:               e.desc("gotwamp_sender_packets_duplicate_total", "Duplicate replies, by the path that duplicated them.", "direction"),
		reordered:         e.desc("gotwamp_sender_packets_reordered_total", "Replies reordered (RFC 4737)."),
		invalidRTT:        e.desc("gotwamp_sender_invalid_rtt_total", "Replies with a negative RTT, left out of the statistics (a clock step)."),
		negativeOWD:       e.desc("gotwamp_sender_negative_owd_total", "Replies with a negative one-way delay, a sign of unsynchronized clocks."),
		txMissing:         e.desc("gotwamp_sender_tx_timestamp_missing_total", "Replies whose kernel transmit timestamp did not arrive in time."),
		txInvalid:         e.desc("gotwamp_sender_tx_timestamp_invalid_total", "Replies whose kernel transmit timestamp was rejected as inconsistent, so that the user-space T1 was used."),
		dscpRemarked:      e.desc("gotwamp_sender_dscp_remarked_total", "Test packets whose DSCP the forward path changed (RFC 7750)."),
		ecnCE:             e.desc("gotwamp_sender_ecn_ce_total", "Test packets marked Congestion Experienced on the forward path (RFC 7750)."),
		lastDSCP:          e.desc("gotwamp_sender_last_run_dscp_reflected", "Most frequent DSCP the reflector saw in the latest run or window (RFC 7750)."),
		stray:             e.desc("gotwamp_sender_packets_stray_total", "Replies discarded as too short, for no packet sent, or after the timeout."),
		reflectedMismatch: e.desc("gotwamp_sender_reflected_mismatch_total", "Replies that did not return the padding asked to be reflected (RFC 6038 Reflect Octets)."),
		unsupported:       e.desc("gotwamp_sender_unsupported_timestamp_format_total", "Replies with timestamps in an unsupported format (PTPv2), left out of the delays."),
		rtt:               e.desc("gotwamp_sender_rtt_seconds", "Round-trip time excluding the reflector's processing time."),
		forward:           e.desc("gotwamp_sender_forward_delay_seconds", "Forward one-way delay, of packets with valid one-way delays."),
		reverse:           e.desc("gotwamp_sender_reverse_delay_seconds", "Reverse one-way delay, of packets with valid one-way delays."),
		refl:              e.desc("gotwamp_sender_reflector_delay_seconds", "Reflector processing time, T3 - T2."),
		setup:             e.desc("gotwamp_sender_control_setup_seconds", "TWAMP-Control setup time, from connect to Start-Ack."),
		lastRTT:           e.desc("gotwamp_sender_last_run_rtt_seconds", "RTT statistics of the latest run or window.", "stat"),
		lastForward:       e.desc("gotwamp_sender_last_run_forward_delay_seconds", "Forward delay statistics of the latest run or window, when valid.", "stat"),
		lastReverse:       e.desc("gotwamp_sender_last_run_reverse_delay_seconds", "Reverse delay statistics of the latest run or window, when valid.", "stat"),
		lastJitter:        e.desc("gotwamp_sender_last_run_jitter_seconds", "Delay variation of the latest run or window.", "kind"),
		lastLoss:          e.desc("gotwamp_sender_last_run_loss_ratio", "Loss ratio of the latest run or window."),
		lastTime:          e.desc("gotwamp_sender_last_run_timestamp_seconds", "End time of the latest run or window, as a Unix time."),
		lastOK:            e.desc("gotwamp_sender_last_run_success", "1 when the latest run completed, 0 when it failed."),
		lastOWDValid:      e.desc("gotwamp_sender_last_run_owd_valid", "1 when the one-way delays of the latest run are valid."),
		hops:              e.desc("gotwamp_sender_path_hops", "Most frequent hop count of the latest run.", "direction"),
		runs:              e.desc("gotwamp_sender_runs_total", "Runs and windows, by result.", "result"),
		runErrors:         e.desc("gotwamp_sender_run_errors_total", "Failed runs, by the stage that failed.", "stage"),
	}
}

func with(lv []string, extra string) []string {
	return append(slices.Clone(lv), extra)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *Collector) collectSession(e emitter, d descs, m *sessionMetrics, lv []string) {
	counter, gauge := prometheus.CounterValue, prometheus.GaugeValue
	e.value(d.sent, counter, float64(m.sent), lv...)
	e.value(d.received, counter, float64(m.received), lv...)
	e.value(d.lost, counter, float64(m.lost), lv...)
	e.value(d.dup, counter, float64(m.dupForward), with(lv, "forward")...)
	e.value(d.dup, counter, float64(m.dupReverse), with(lv, "reverse")...)
	e.value(d.reordered, counter, float64(m.reordered), lv...)
	e.value(d.invalidRTT, counter, float64(m.invalidRTT), lv...)
	e.value(d.negativeOWD, counter, float64(m.negativeOWD), lv...)
	e.value(d.txMissing, counter, float64(m.txMissing), lv...)
	e.value(d.txInvalid, counter, float64(m.txInvalid), lv...)
	e.value(d.unsupported, counter, float64(m.unsupported), lv...)
	e.value(d.stray, counter, float64(m.stray), lv...)
	e.value(d.reflectedMismatch, counter, float64(m.reflectedMismatch), lv...)
	e.value(d.dscpRemarked, counter, float64(m.dscpRemarked), lv...)
	e.value(d.ecnCE, counter, float64(m.ecnCE), lv...)
	e.ch <- m.rtt.metric(d.rtt, c.buckets, lv)
	e.ch <- m.forward.metric(d.forward, c.buckets, lv)
	e.ch <- m.reverse.metric(d.reverse, c.buckets, lv)
	e.ch <- m.refl.metric(d.refl, c.buckets, lv)
	if m.info.Mode == "full" {
		e.ch <- m.controlSetup.metric(d.setup, c.buckets, lv)
	}
	e.value(d.runs, counter, float64(m.runsOK), with(lv, "success")...)
	e.value(d.runs, counter, float64(m.runsFailed), with(lv, "error")...)
	e.value(d.runs, counter, float64(m.runsSkip), with(lv, "skipped")...)
	stages := make([]string, 0, len(m.runErrors))
	for s := range m.runErrors {
		stages = append(stages, s)
	}
	slices.Sort(stages)
	for _, s := range stages {
		e.value(d.runErrors, counter, float64(m.runErrors[s]), with(lv, s)...)
	}
	if m.lastSuccessSet {
		e.value(d.lastOK, gauge, boolValue(m.lastSuccess), lv...)
		e.value(d.lastTime, gauge, float64(m.lastTime.UnixNano())/1e9, lv...)
	}

	s := m.last
	if s == nil {
		return
	}
	e.value(d.lastLoss, gauge, s.LossRatio, lv...)
	e.value(d.lastOWDValid, gauge, boolValue(s.OWDValid), lv...)
	stat(e, d.lastRTT, s.RTT, lv)
	if s.OWDValid {
		stat(e, d.lastForward, s.Forward, lv)
		stat(e, d.lastReverse, s.Reverse, lv)
	}
	j := s.Jitter
	jitter := []struct {
		kind    string
		v       time.Duration
		defined bool
	}{
		{"ipdv_mean", j.IPDVRTT.Mean, j.IPDVRTT.Count > 0},
		{"ipdv_max", j.IPDVRTT.Max, j.IPDVRTT.Count > 0},
		{"pdv_p95", j.PDV.P95, j.PDV.Count > 0},
		{"pdv_p99", j.PDV.P99, j.PDV.Count > 0},
		{"rfc3550", j.RFC3550, s.RTT.Count > 1},
		{"p95_p50", j.P95P50, s.RTT.Count > 0},
	}
	for _, x := range jitter {
		if x.defined {
			e.value(d.lastJitter, gauge, x.v.Seconds(), with(lv, x.kind)...)
		}
	}
	if s.DSCPReflected.Count > 0 {
		e.value(d.lastDSCP, gauge, float64(s.DSCPReflected.Mode), lv...)
	}
	if s.ForwardHops.Count > 0 {
		e.value(d.hops, gauge, float64(s.ForwardHops.Mode), with(lv, "forward")...)
	}
	if s.ReverseHops.Count > 0 {
		e.value(d.hops, gauge, float64(s.ReverseHops.Mode), with(lv, "reverse")...)
	}
}

// stat exports the statistics of one distribution; an empty one has none
// (SPEC §7.5).
func stat(e emitter, d *prometheus.Desc, dist stats.Distribution, lv []string) {
	if dist.Count == 0 {
		return
	}
	for _, s := range []struct {
		name string
		v    time.Duration
	}{
		{"min", dist.Min},
		{"mean", dist.Mean},
		{"median", dist.Median},
		{"max", dist.Max},
		{"p95", dist.P95},
		{"p99", dist.P99},
		{"stddev", dist.StdDev},
	} {
		e.value(d, prometheus.GaugeValue, s.v.Seconds(), with(lv, s.name)...)
	}
}

// collectDaemon exports the reflector, clock and build metrics, which carry
// no session labels.
func (c *Collector) collectDaemon(sess emitter) {
	e := emitter{ch: sess.ch}
	counter, gauge := prometheus.CounterValue, prometheus.GaugeValue
	if src := c.cfg.Reflector; src != nil {
		snap := src.ReflectorSnapshot()
		recv := e.desc("gotwamp_reflector_packets_received_total", "Test packets received by the reflector.", "kind", "listener")
		refl := e.desc("gotwamp_reflector_packets_reflected_total", "Test packets reflected.", "kind", "listener")
		for _, l := range snap.Listeners {
			e.value(recv, counter, float64(l.Received), l.Kind, l.Listener)
			e.value(refl, counter, float64(l.Reflected), l.Kind, l.Listener)
		}
		drop := e.desc("gotwamp_reflector_packets_dropped_total", "Test packets dropped by the reflector, by reason.", "kind", "reason")
		for _, x := range snap.Dropped {
			e.value(drop, counter, float64(x.Count), x.Kind, x.Reason)
		}
		e.value(e.desc("gotwamp_reflector_control_connections_active", "Open TWAMP-Control connections."),
			gauge, float64(snap.ControlConnectionsActive))
		conns := e.desc("gotwamp_reflector_control_connections_total", "TWAMP-Control connections, by result.", "result")
		for _, k := range sortedKeys(snap.ControlConnections) {
			e.value(conns, counter, float64(snap.ControlConnections[k]), k)
		}
		active := e.desc("gotwamp_reflector_sessions_active", "Full sessions and Light flows with state.", "kind")
		for _, k := range sortedKeys(snap.SessionsActive) {
			e.value(active, gauge, float64(snap.SessionsActive[k]), k)
		}
		e.value(e.desc("gotwamp_reflector_light_flows_overflow_total", "Packets of new Light flows handled statelessly because the flow table was full."),
			counter, float64(snap.LightFlowsOverflow))
	}
	if src := c.cfg.Clock; src != nil {
		synced, maxErr := src.Status()
		e.value(e.desc("gotwamp_clock_synchronized", "1 when the local clock is synchronized."), gauge, boolValue(synced))
		e.value(e.desc("gotwamp_clock_max_error_seconds", "Maximum error of the local clock."), gauge, maxErr.Seconds())
	}
	if c.cfg.Timestamping != "" {
		e.value(e.desc("gotwamp_timestamping_mode", "The timestamping mode in effect.", "mode"), gauge, 1, c.cfg.Timestamping)
	}
	if c.cfg.JSONLDropped != nil {
		e.value(e.desc("gotwamp_jsonl_dropped_total", "JSON Lines records dropped because the writer fell behind."),
			counter, float64(c.cfg.JSONLDropped()))
	}
	if c.cfg.Store != nil {
		s := c.cfg.Store()
		e.value(e.desc("gotwamp_store_results_written_total", "Results committed to the result store's database."), counter, float64(s.Written))
		e.value(e.desc("gotwamp_store_results_dropped_total", "Results not kept because the database writer was behind."), counter, float64(s.Dropped))
		e.value(e.desc("gotwamp_store_results_failed_total", "Results the result store's database refused."), counter, float64(s.Failed))
	}
	e.value(e.desc("gotwamp_build_info", "Build information.", "version", "commit", "go_version"),
		gauge, 1, c.cfg.Version, c.cfg.Commit, runtime.Version())
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Handler serves /metrics, with the Go runtime and process metrics, and
// /healthz.
func (c *Collector) Handler() http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(c, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if c.cfg.Health != nil {
			if err := c.cfg.Health(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Length", strconv.Itoa(3))
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
