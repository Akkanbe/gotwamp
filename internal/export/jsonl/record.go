package jsonl

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// The record layouts of SPEC §9.7. Durations are seconds as floats; a
// statistic that is undefined (no samples) is null.

type header struct {
	Type    string `json:"type"`
	Time    string `json:"time"`
	Session string `json:"session"`
	RunID   string `json:"run_id"`
}

type specRecord struct {
	Target   string `json:"target"`
	Mode     string `json:"mode"`
	Count    uint32 `json:"count"`
	Interval string `json:"interval"`
	Padding  int    `json:"padding"`
	DSCP     int    `json:"dscp"`
}

type localRecord struct {
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

type remoteRecord struct {
	Address     string `json:"address"`
	ControlPort uint16 `json:"control_port,omitempty"`
	Port        uint16 `json:"port"`
}

type delayRecord struct {
	Min    float64 `json:"min"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	StdDev float64 `json:"stddev"`
	// Samples is how many values the percentiles came from; fewer than the
	// values observed when the series was decimated (SPEC §7.5).
	Samples int `json:"samples"`
}

type jitterRecord struct {
	IPDVRTTMean   *float64 `json:"ipdv_rtt_mean"`
	IPDVRTTMax    *float64 `json:"ipdv_rtt_max"`
	IPDVRTTP95    *float64 `json:"ipdv_rtt_p95"`
	IPDVFwdMean   *float64 `json:"ipdv_fwd_mean"`
	IPDVFwdMax    *float64 `json:"ipdv_fwd_max"`
	IPDVFwdP95    *float64 `json:"ipdv_fwd_p95"`
	IPDVRevMean   *float64 `json:"ipdv_rev_mean"`
	IPDVRevMax    *float64 `json:"ipdv_rev_max"`
	IPDVRevP95    *float64 `json:"ipdv_rev_p95"`
	PDVRTTP95     *float64 `json:"pdv_rtt_p95"`
	PDVRTTP99     *float64 `json:"pdv_rtt_p99"`
	PDVRTTP999    *float64 `json:"pdv_rtt_p999"`
	JitterRFC3550 *float64 `json:"jitter_rfc3550"`
	JitterP95P50  *float64 `json:"jitter_p95_p50"`
}

type hopsRecord struct {
	Fwd *int `json:"fwd"`
	Rev *int `json:"rev"`
}

type clockRecord struct {
	SenderSynced    bool    `json:"sender_synced"`
	ReflectorSynced bool    `json:"reflector_synced"`
	MaxClockError   float64 `json:"max_clock_error"`
	// MaxClockErrorUnknown: an end sent Multiplier 0, so max_clock_error
	// does not bound the error (SPEC §7.6).
	MaxClockErrorUnknown bool   `json:"max_clock_error_unknown"`
	Timestamping         string `json:"timestamping"`
	// TxTimestamps: the sender used kernel transmit timestamps (T1').
	TxTimestamps bool `json:"tx_timestamps"`
}

// dscpRecord summarizes the DSCP and ECN the reflector saw (RFC 7750).
type dscpRecord struct {
	Count    uint64 `json:"count"`
	Mode     uint8  `json:"mode"`
	Remarked uint64 `json:"remarked"`
	CE       uint64 `json:"ce"`
}

type summaryRecord struct {
	header
	Spec             specRecord   `json:"spec"`
	Local            localRecord  `json:"local"`
	Remote           remoteRecord `json:"remote"`
	SID              string       `json:"sid,omitempty"`
	Window           *int         `json:"window,omitempty"`
	StartedAt        string       `json:"started_at"`
	EndedAt          string       `json:"ended_at"`
	Aborted          bool         `json:"aborted"`
	Invalid          bool         `json:"invalid"`
	Sent             uint64       `json:"sent"`
	Received         uint64       `json:"received"`
	Lost             uint64       `json:"lost"`
	Pending          uint64       `json:"pending"`
	SendErrors       uint64       `json:"send_errors"`
	LossRatio        float64      `json:"loss_ratio"`
	DupForward       uint64       `json:"dup_forward"`
	DupReverse       uint64       `json:"dup_reverse"`
	Reordered        uint64       `json:"reordered"`
	ReorderedReverse uint64       `json:"reordered_reverse"`
	LostForward      *uint64      `json:"lost_forward_est"`
	LostReverse      *uint64      `json:"lost_reverse_est"`
	LossSplitValid   bool         `json:"loss_split_valid"`
	Late             uint64       `json:"late"`
	Stray            uint64       `json:"stray"`
	InvalidRTT       uint64       `json:"invalid_rtt"`
	NegativeOWD      uint64       `json:"negative_owd"`
	Unsupported      uint64       `json:"unsupported_timestamp_format"`
	TxMissing        uint64       `json:"tx_timestamp_missing"`
	TxInvalid        uint64       `json:"tx_timestamp_invalid"`
	// ReflectedMismatch counts the replies that did not return the padding
	// asked to be reflected (RFC 6038 Reflect Octets).
	ReflectedMismatch uint64       `json:"reflected_mismatch"`
	RTT               *delayRecord `json:"rtt"`
	FwdDelay          *delayRecord `json:"fwd_delay"`
	RevDelay          *delayRecord `json:"rev_delay"`
	ReflectorDelay    *delayRecord `json:"reflector_delay"`
	OWDValid          bool         `json:"owd_valid"`
	Jitter            jitterRecord `json:"jitter"`
	Hops              hopsRecord   `json:"hops"`
	DSCPReflected     *dscpRecord  `json:"dscp_reflected"`
	Clock             clockRecord  `json:"clock"`
}

type packetRecord struct {
	header
	Seq          uint32 `json:"seq"`
	ReflectorSeq uint32 `json:"reflector_seq"`
	Window       int    `json:"window"`
	T1           string `json:"t1"`
	T2           string `json:"t2"`
	T3           string `json:"t3"`
	T4           string `json:"t4"`
	// The delays are null when the timestamps were in a format the sender
	// could not convert.
	RTT             *float64 `json:"rtt"`
	RTTRaw          *float64 `json:"rtt_raw"`
	FwdDelay        *float64 `json:"fwd_delay"`
	RevDelay        *float64 `json:"rev_delay"`
	ReflectorDelay  *float64 `json:"reflector_delay"`
	ClockOffsetHint *float64 `json:"clock_offset_hint"`
	OWDValid        bool     `json:"owd_valid"`
	T1FromKernel    bool     `json:"t1_from_kernel"`
	SenderTTL       uint8    `json:"sender_ttl"`
	ReflectorTTL    *uint8   `json:"reflector_ttl"`
	DSCPReflected   *uint8   `json:"dscp_reflected"`
	ECNReflected    *uint8   `json:"ecn_reflected"`
	Kind            string   `json:"kind"`
	Dup             bool     `json:"dup"`
	Reordered       bool     `json:"reordered"`
}

type errorRecord struct {
	header
	Stage   string `json:"stage"`
	Message string `json:"message"`
	Accept  int    `json:"accept"`
}

// encode returns the record of e followed by a newline.
//
//declscope:package // the writer goroutine encodes every record here
func encode(e store.Event, loc *time.Location) ([]byte, error) {
	h := header{Type: e.Kind.String(), Time: stamp(e.Time, loc), Session: e.Session.Name, RunID: e.RunID}
	var v any
	switch e.Kind {
	case store.EventRunSummary, store.EventWindowSummary:
		v = summary(h, e, loc)
	case store.EventPacket:
		v = packet(h, e.Packet, loc)
	case store.EventRunError:
		v = errorRecord{header: h, Stage: e.Error.Stage, Message: e.Error.Message, Accept: e.Error.Accept}
	default:
		return nil, fmt.Errorf("jsonl: no record for %v events", e.Kind)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jsonl: encode %v: %w", e.Kind, err)
	}
	return append(b, '\n'), nil
}

// timeLayout is RFC 3339 with all nine digits of nanoseconds (SPEC §9.7).
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func stamp(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(timeLayout)
}

func seconds(d time.Duration) float64 { return d.Seconds() }

func optional(d time.Duration, defined bool) *float64 {
	if !defined {
		return nil
	}
	v := d.Seconds()
	return &v
}

func delay(d stats.Distribution) *delayRecord {
	if d.Count == 0 {
		return nil
	}
	return &delayRecord{
		Min: seconds(d.Min), Mean: seconds(d.Mean), Median: seconds(d.Median), Max: seconds(d.Max),
		P95: seconds(d.P95), P99: seconds(d.P99), StdDev: seconds(d.StdDev), Samples: d.Samples,
	}
}

func hops(h stats.Hops) *int {
	if h.Count == 0 {
		return nil
	}
	v := h.Mode
	return &v
}

func summary(h header, e store.Event, loc *time.Location) summaryRecord {
	s := e.Summary
	info := e.Session
	j := s.Jitter
	r := summaryRecord{
		header: h,
		Spec: specRecord{
			Target: info.Target, Mode: info.Mode, Count: info.Count,
			Interval: info.Interval.String(), Padding: info.Padding, DSCP: info.DSCP,
		},
		Local:             localRecord{Address: s.Local.Addr().String(), Port: s.Local.Port()},
		Remote:            remoteRecord{Address: s.Remote.Addr().String(), ControlPort: info.ControlPort, Port: s.Remote.Port()},
		SID:               hex.EncodeToString(e.SID),
		StartedAt:         stamp(s.StartedAt, loc),
		EndedAt:           stamp(s.EndedAt, loc),
		Aborted:           s.Aborted,
		Invalid:           s.Invalid,
		Sent:              s.Sent,
		Received:          s.Received,
		Lost:              s.Lost,
		Pending:           s.Pending,
		SendErrors:        s.SendErrors,
		LossRatio:         s.LossRatio,
		DupForward:        s.DupForward,
		DupReverse:        s.DupReverse,
		Reordered:         s.Reordered,
		ReorderedReverse:  s.ReorderedReverse,
		LossSplitValid:    s.LossSplitValid,
		Late:              s.Late,
		Stray:             s.Stray,
		InvalidRTT:        s.InvalidRTT,
		NegativeOWD:       s.NegativeOWD,
		Unsupported:       s.Unsupported,
		TxMissing:         s.TxMissing,
		TxInvalid:         s.TxInvalid,
		ReflectedMismatch: s.ReflectedMismatch,
		RTT:               delay(s.RTT),
		FwdDelay:          delay(s.Forward),
		RevDelay:          delay(s.Reverse),
		ReflectorDelay:    delay(s.ReflectorDelay),
		OWDValid:          s.OWDValid,
		Jitter: jitterRecord{
			IPDVRTTMean:   optional(j.IPDVRTT.Mean, j.IPDVRTT.Count > 0),
			IPDVRTTMax:    optional(j.IPDVRTT.Max, j.IPDVRTT.Count > 0),
			IPDVRTTP95:    optional(j.IPDVRTT.P95, j.IPDVRTT.Count > 0),
			IPDVFwdMean:   optional(j.IPDVForward.Mean, j.IPDVForward.Count > 0),
			IPDVFwdMax:    optional(j.IPDVForward.Max, j.IPDVForward.Count > 0),
			IPDVFwdP95:    optional(j.IPDVForward.P95, j.IPDVForward.Count > 0),
			IPDVRevMean:   optional(j.IPDVReverse.Mean, j.IPDVReverse.Count > 0),
			IPDVRevMax:    optional(j.IPDVReverse.Max, j.IPDVReverse.Count > 0),
			IPDVRevP95:    optional(j.IPDVReverse.P95, j.IPDVReverse.Count > 0),
			PDVRTTP95:     optional(j.PDV.P95, j.PDV.Count > 0),
			PDVRTTP99:     optional(j.PDV.P99, j.PDV.Count > 0),
			PDVRTTP999:    optional(j.PDV.P999, j.PDV.Count > 0),
			JitterRFC3550: optional(j.RFC3550, s.RTT.Count > 1),
			JitterP95P50:  optional(j.P95P50, s.RTT.Count > 0),
		},
		Hops: hopsRecord{Fwd: hops(s.ForwardHops), Rev: hops(s.ReverseHops)},
		Clock: clockRecord{
			SenderSynced: s.SenderSynced, ReflectorSynced: s.ReflectorSynced,
			MaxClockError: seconds(s.MaxClockError), MaxClockErrorUnknown: s.MaxClockErrorUnknown,
			Timestamping: s.Timestamping.String(), TxTimestamps: s.TxTimestamps,
		},
	}
	if s.LossSplitValid {
		r.LostForward, r.LostReverse = &s.LostForward, &s.LostReverse
	}
	if d := s.DSCPReflected; d.Count > 0 {
		r.DSCPReflected = &dscpRecord{Count: d.Count, Mode: d.Mode, Remarked: d.Remarked, CE: d.CE}
	}
	if e.Kind == store.EventWindowSummary {
		r.Window = &s.Window
	}
	return r
}

func packet(h header, p sender.PacketResult, loc *time.Location) packetRecord {
	m := p.Result.Metrics
	r := packetRecord{
		header:          h,
		Seq:             p.Record.SenderSeq,
		ReflectorSeq:    p.Record.ReflectorSeq,
		Window:          p.Window,
		T1:              stamp(p.Record.T1, loc),
		T2:              stamp(p.Record.T2, loc),
		T3:              stamp(p.Record.T3, loc),
		T4:              stamp(p.Record.T4, loc),
		RTT:             optional(m.RTT, m.DelayUsable),
		RTTRaw:          optional(m.RTTRaw, m.DelayUsable),
		FwdDelay:        optional(m.Forward, m.DelayUsable),
		RevDelay:        optional(m.Reverse, m.DelayUsable),
		ReflectorDelay:  optional(m.ReflectorDelay, m.DelayUsable),
		ClockOffsetHint: optional(m.ClockOffset, m.DelayUsable),
		OWDValid:        m.OWDValid,
		T1FromKernel:    p.T1FromKernel,
		SenderTTL:       p.Record.SenderTTL,
		Kind:            p.Result.Kind.String(),
		Dup:             p.Result.Kind == stats.KindDupForward || p.Result.Kind == stats.KindDupReverse,
		Reordered:       p.Result.Reordered,
	}
	if p.Record.ReplyTTL != 0 {
		ttl := p.Record.ReplyTTL
		r.ReflectorTTL = &ttl
	}
	if p.Record.HasDSCPECN {
		dscp, ecn := p.Record.DSCPECN>>2, p.Record.DSCPECN&3
		r.DSCPReflected, r.ECNReflected = &dscp, &ecn
	}
	return r
}
