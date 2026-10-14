package jsonl

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

var (
	jst   = time.FixedZone("JST", 9*3600)
	start = time.Date(2026, 10, 3, 3, 0, 0, 123456789, time.UTC)
	info  = &store.SessionInfo{
		Name: "tokyo-osaka", Target: "192.0.2.10", Mode: "full", Count: 100,
		Interval: 100 * time.Millisecond, Padding: 27, DSCP: 46, ControlPort: 862,
	}
)

func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	if line[len(line)-1] != '\n' {
		t.Fatalf("line %q does not end with a newline", line)
	}
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("decode %s: %v", line, err)
	}
	return m
}

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

func TestSummaryRecord(t *testing.T) {
	d := stats.Distribution{Count: 3, Samples: 3, Min: ms(8), Mean: ms(9), Median: ms(9), Max: ms(10), P95: ms(10), P99: ms(10), StdDev: ms(1)}
	s := &sender.Summary{
		Summary: stats.Summary{
			Sent: 100, Received: 98, Lost: 2, LossRatio: 0.02, DupForward: 1,
			LossSplitValid: true, LostForward: 2, RTT: d, Forward: d,
			OWDValid: true, SenderSynced: true, MaxClockError: 210 * time.Microsecond,
			Jitter:      stats.Jitter{IPDVRTT: d, PDV: d, RFC3550: ms(1), P95P50: ms(1)},
			ForwardHops: stats.Hops{Count: 98, Mode: 4},
			NegativeOWD: 1, Unsupported: 2,
			MaxClockErrorUnknown: true,
			DSCPReflected:        stats.DSCPReflected{Count: 98, Mode: 46, Remarked: 2, CE: 1},
		},
		Invalid:           true,
		TxTimestamps:      true,
		TxMissing:         3,
		TxInvalid:         4,
		ReflectedMismatch: 5,
		StartedAt:         start,
		EndedAt:           start.Add(10 * time.Second),
		Local:             netip.MustParseAddrPort("203.0.113.5:51234"),
		Remote:            netip.MustParseAddrPort("192.0.2.10:51234"),
		Timestamping:      tstamp.ModeKernel,
	}
	e := store.Event{Kind: store.EventRunSummary, Time: s.EndedAt, Session: info, RunID: "01J", SID: []byte{0xc0, 0, 2, 5}, Summary: s}
	line, err := encode(e, jst)
	if err != nil {
		t.Fatal(err)
	}
	m := decode(t, line)
	checks := map[string]any{
		"type": "run_summary", "time": "2026-10-03T12:00:10.123456789+09:00", "session": "tokyo-osaka", "run_id": "01J",
		"sid": "c0000205", "started_at": "2026-10-03T12:00:00.123456789+09:00", "aborted": false,
		"sent": 100.0, "received": 98.0, "lost": 2.0, "loss_ratio": 0.02, "dup_forward": 1.0,
		"lost_forward_est": 2.0, "lost_reverse_est": 0.0, "loss_split_valid": true, "owd_valid": true,
		"rev_delay": nil, "reflector_delay": nil,
		"invalid": true, "negative_owd": 1.0, "unsupported_timestamp_format": 2.0,
		"tx_timestamp_missing": 3.0, "tx_timestamp_invalid": 4.0, "reflected_mismatch": 5.0,
	}
	for k, want := range checks {
		if m[k] != want {
			t.Errorf("%s = %v, want %v", k, m[k], want)
		}
	}
	if _, ok := m["window"]; ok {
		t.Error("run summary has a window")
	}
	spec := m["spec"].(map[string]any)
	if spec["target"] != "192.0.2.10" || spec["mode"] != "full" || spec["interval"] != "100ms" || spec["dscp"] != 46.0 {
		t.Errorf("spec = %v", spec)
	}
	if l, r := m["local"].(map[string]any), m["remote"].(map[string]any); l["address"] != "203.0.113.5" ||
		l["port"] != 51234.0 || r["control_port"] != 862.0 || r["port"] != 51234.0 {
		t.Errorf("endpoints %v %v", l, r)
	}
	rtt := m["rtt"].(map[string]any)
	if rtt["min"] != 0.008 || rtt["median"] != 0.009 || rtt["stddev"] != 0.001 || rtt["samples"] != 3.0 || len(rtt) != 8 {
		t.Errorf("rtt = %v", rtt)
	}
	j := m["jitter"].(map[string]any)
	if j["ipdv_fwd_max"] != nil || j["ipdv_rev_p95"] != nil {
		t.Errorf("IPDV of the directions without samples: %v", j)
	}
	if j["ipdv_rtt_mean"] != 0.009 || j["pdv_rtt_p99"] != 0.01 || j["jitter_rfc3550"] != 0.001 ||
		j["ipdv_fwd_mean"] != nil || j["jitter_p95_p50"] != 0.001 {
		t.Errorf("jitter = %v", j)
	}
	if h := m["hops"].(map[string]any); h["fwd"] != 4.0 || h["rev"] != nil {
		t.Errorf("hops = %v", h)
	}
	if d := m["dscp_reflected"].(map[string]any); d["count"] != 98.0 || d["mode"] != 46.0 || d["remarked"] != 2.0 || d["ce"] != 1.0 {
		t.Errorf("dscp_reflected = %v", d)
	}
	if c := m["clock"].(map[string]any); c["max_clock_error_unknown"] != true {
		t.Errorf("clock = %v", c)
	}
	// tx_timestamps belongs to clock (SPEC §9.7), not to the top level.
	if _, ok := m["tx_timestamps"]; ok {
		t.Error("tx_timestamps at the top level")
	}
	if c := m["clock"].(map[string]any); c["tx_timestamps"] != true {
		t.Errorf("clock.tx_timestamps = %v, want true", c["tx_timestamps"])
	}
	if c := m["clock"].(map[string]any); c["sender_synced"] != true || c["max_clock_error"] != 0.00021 || c["timestamping"] != "kernel" {
		t.Errorf("clock = %v", c)
	}

	// A window summary names its window; an empty one has no statistics.
	empty := &sender.Summary{Window: 3, StartedAt: start, EndedAt: start}
	line, err = encode(store.Event{Kind: store.EventWindowSummary, Time: start, Session: info, Summary: empty}, jst)
	if err != nil {
		t.Fatal(err)
	}
	m = decode(t, line)
	if m["type"] != "window_summary" || m["window"] != 3.0 || m["rtt"] != nil || m["dscp_reflected"] != nil || m["lost_forward_est"] != nil || m["sid"] != nil {
		t.Errorf("window record %s", line)
	}
	if j := m["jitter"].(map[string]any); j["jitter_rfc3550"] != nil || j["jitter_p95_p50"] != nil {
		t.Errorf("empty jitter %v", j)
	}
}

func TestPacketRecord(t *testing.T) {
	p := sender.PacketResult{
		Window: 2,
		Record: stats.PacketRecord{
			SenderSeq: 7, ReflectorSeq: 6, T1: start, T2: start.Add(ms(4)), T3: start.Add(ms(4.5)), T4: start.Add(ms(8.5)),
			SenderTTL: 251, ReplyTTL: 252, DSCPECN: 46<<2 | 1, HasDSCPECN: true,
		},
		Result: stats.Result{
			Kind: stats.KindDupForward,
			Metrics: stats.Metrics{
				RTT: ms(8), RTTRaw: ms(8.5), Forward: ms(4), Reverse: ms(4), ReflectorDelay: ms(0.5),
				ClockOffset: ms(-0.25), DelayUsable: true, OWDValid: true,
			},
			Reordered: true,
		},
		T1FromKernel: true,
	}
	line, err := encode(store.Event{Kind: store.EventPacket, Time: p.Record.T4, Session: info, RunID: "r", Packet: p}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"packet","time":"2026-10-03T03:00:00.131956789Z","session":"tokyo-osaka","run_id":"r",` +
		`"seq":7,"reflector_seq":6,"window":2,"t1":"2026-10-03T03:00:00.123456789Z","t2":"2026-10-03T03:00:00.127456789Z",` +
		`"t3":"2026-10-03T03:00:00.127956789Z","t4":"2026-10-03T03:00:00.131956789Z","rtt":0.008,"rtt_raw":0.0085,"fwd_delay":0.004,` +
		`"rev_delay":0.004,"reflector_delay":0.0005,"clock_offset_hint":-0.00025,"owd_valid":true,"t1_from_kernel":true,` +
		`"sender_ttl":251,"reflector_ttl":252,"dscp_reflected":46,"ecn_reflected":1,` +
		`"kind":"duplicate_forward","dup":true,"reordered":true}` + "\n"
	if string(line) != want {
		t.Errorf("packet record\n got %s\nwant %s", line, want)
	}

	// Timestamps in an unsupported format leave the delays undefined.
	p.Record.ReplyTTL = 0
	p.Record.HasDSCPECN = false
	p.Result.Kind = stats.KindAccepted
	p.Result.Metrics = stats.Metrics{}
	line, _ = encode(store.Event{Kind: store.EventPacket, Session: &store.SessionInfo{}, Packet: p}, time.UTC)
	if m := decode(t, line); m["reflector_ttl"] != nil || m["dup"] != false || m["session"] != "" ||
		m["rtt"] != nil || m["rtt_raw"] != nil || m["clock_offset_hint"] != nil || m["dscp_reflected"] != nil {
		t.Errorf("packet record %s", line)
	}
}

func TestErrorRecord(t *testing.T) {
	e := store.Event{
		Kind: store.EventRunError, Time: start, Session: info, RunID: "01J",
		Error: &store.RunError{Stage: "request", Message: "accept-session accept=5 (temporary resource limitation)", Accept: 5},
	}
	line, err := encode(e, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"run_error","time":"2026-10-03T03:00:00.123456789Z","session":"tokyo-osaka","run_id":"01J",` +
		`"stage":"request","message":"accept-session accept=5 (temporary resource limitation)","accept":5}` + "\n"
	if string(line) != want {
		t.Errorf("error record\n got %s\nwant %s", line, want)
	}
	if _, err := encode(store.Event{Kind: store.EventLost, Session: info}, time.UTC); err == nil {
		t.Error("lost events have no record")
	}
}

func BenchmarkEncodeSummary(b *testing.B) {
	d := stats.Distribution{Count: 3, Min: ms(8), Mean: ms(9), Median: ms(9), Max: ms(10), P95: ms(10), P99: ms(10), StdDev: ms(1)}
	s := &sender.Summary{Summary: stats.Summary{Sent: 100, Received: 100, RTT: d, Forward: d, Reverse: d, ReflectorDelay: d}, StartedAt: start, EndedAt: start}
	e := store.Event{Kind: store.EventRunSummary, Time: start, Session: info, RunID: "01J", Summary: s}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := encode(e, time.UTC); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodePacket(b *testing.B) {
	p := sender.PacketResult{Record: stats.PacketRecord{SenderSeq: 7, T1: start, T2: start, T3: start, T4: start, ReplyTTL: 64}}
	e := store.Event{Kind: store.EventPacket, Time: start, Session: info, RunID: "01J", Packet: p}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := encode(e, time.UTC); err != nil {
			b.Fatal(err)
		}
	}
}
