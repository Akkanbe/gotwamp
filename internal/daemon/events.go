package daemon

import (
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// packetEvent converts a reply into a TestEvent. Strays and late replies,
// which belong to no outstanding packet, yield nil.
//
//declscope:package // RunTest streams every reply through it
func packetEvent(r sender.PacketResult) *gotwampv1.TestEvent {
	var kind gotwampv1.DuplicateKind
	switch r.Result.Kind {
	case stats.KindAccepted:
	case stats.KindDupForward:
		kind = gotwampv1.DuplicateKind_DUPLICATE_KIND_FORWARD
	case stats.KindDupReverse:
		kind = gotwampv1.DuplicateKind_DUPLICATE_KIND_REVERSE
	default:
		return nil
	}
	rec, m := r.Record, r.Result.Metrics
	pr := &gotwampv1.PacketResult{
		Seq:                  rec.SenderSeq,
		ReflectorSeq:         rec.ReflectorSeq,
		T1:                   timestamppb.New(rec.T1),
		T2:                   timestamppb.New(rec.T2),
		T3:                   timestamppb.New(rec.T3),
		T4:                   timestamppb.New(rec.T4),
		OwdValid:             m.OWDValid,
		SenderTtlAtReflector: uint32(rec.SenderTTL),
		ReflectorTtlReceived: uint32(rec.ReplyTTL),
		Duplicate:            kind != gotwampv1.DuplicateKind_DUPLICATE_KIND_UNSPECIFIED,
		DuplicateKind:        kind,
		Reordered:            r.Result.Reordered,
		SenderErrest:         protoErrest(r.SenderErrorEstimate),
		ReflectorErrest:      protoErrest(r.ReflectorErrorEstimate),
		T1FromKernel:         r.T1FromKernel,
	}
	if r.TOS >= 0 {
		pr.DscpReceived = uint32(r.TOS >> 2) //nolint:gosec // G115: TOS is 0-255
	}
	if m.DelayUsable {
		pr.Rtt = durationpb.New(m.RTT)
		pr.RttRaw = durationpb.New(m.RTTRaw)
		pr.FwdDelay = durationpb.New(m.Forward)
		pr.RevDelay = durationpb.New(m.Reverse)
		pr.ReflectorDelay = durationpb.New(m.ReflectorDelay)
		pr.ClockOffsetHint = durationpb.New(m.ClockOffset)
	}
	if rec.HasDSCPECN {
		dscp, ecn := uint32(rec.DSCPECN>>2), uint32(rec.DSCPECN&3)
		pr.DscpReflected, pr.EcnReflected = &dscp, &ecn
	}
	return &gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Packet{Packet: pr}}
}

func protoErrest(e errest.Estimate) *gotwampv1.ErrorEstimate {
	pe := &gotwampv1.ErrorEstimate{
		Synchronized: e.Synchronized(),
		PtpFormat:    e.PTP(),
		Scale:        uint32(e.Scale()),
		Multiplier:   uint32(e.Multiplier()),
	}
	if e.Valid() {
		pe.Error = durationpb.New(e.Duration())
	}
	return pe
}

// runSummary converts the summary of a run or window. session is empty for
// an ad-hoc test; sid is nil for Light.
//
//declscope:package // RunTest streams the summary through it
func runSummary(s sender.Summary, session, runID string, spec *gotwampv1.TestSpec, sid []byte, kind gotwampv1.SummaryKind) *gotwampv1.RunSummary {
	j := s.Jitter
	if sid == nil && s.SID != (wire.SID{}) {
		sid = s.SID[:]
	}
	var dscp *gotwampv1.DscpReflected
	if d := s.DSCPReflected; d.Count > 0 {
		dscp = &gotwampv1.DscpReflected{Count: d.Count, Mode: uint32(d.Mode), Remarked: d.Remarked, Ce: d.CE}
	}
	return &gotwampv1.RunSummary{
		Session:          session,
		RunId:            runID,
		Spec:             spec,
		Sid:              sid,
		Local:            protoEndpoint(s.Local),
		Remote:           protoEndpoint(s.Remote),
		StartedAt:        timestamppb.New(s.StartedAt),
		EndedAt:          timestamppb.New(s.EndedAt),
		Sent:             clamp32(s.Sent),
		Received:         clamp32(s.Received),
		Lost:             clamp32(s.Lost),
		LossRatio:        s.LossRatio,
		DupForward:       clamp32(s.DupForward),
		DupReverse:       clamp32(s.DupReverse),
		Reordered:        clamp32(s.Reordered),
		ReorderedReverse: clamp32(s.ReorderedReverse),
		LostForwardEst:   clamp63(s.LostForward),
		LostReverseEst:   clamp63(s.LostReverse),
		LossSplitValid:   s.LossSplitValid,
		Rtt:              delayStats(s.RTT),
		FwdDelay:         delayStats(s.Forward),
		RevDelay:         delayStats(s.Reverse),
		ReflectorDelay:   delayStats(s.ReflectorDelay),
		OwdValid:         s.OWDValid,
		Jitter: &gotwampv1.JitterStats{
			IpdvRttMean:   optDuration(j.IPDVRTT.Count, j.IPDVRTT.Mean),
			IpdvRttMax:    optDuration(j.IPDVRTT.Count, j.IPDVRTT.Max),
			IpdvRttP95:    optDuration(j.IPDVRTT.Count, j.IPDVRTT.P95),
			IpdvFwdMean:   optDuration(j.IPDVForward.Count, j.IPDVForward.Mean),
			IpdvFwdMax:    optDuration(j.IPDVForward.Count, j.IPDVForward.Max),
			IpdvFwdP95:    optDuration(j.IPDVForward.Count, j.IPDVForward.P95),
			IpdvRevMean:   optDuration(j.IPDVReverse.Count, j.IPDVReverse.Mean),
			IpdvRevMax:    optDuration(j.IPDVReverse.Count, j.IPDVReverse.Max),
			IpdvRevP95:    optDuration(j.IPDVReverse.Count, j.IPDVReverse.P95),
			PdvRttP95:     optDuration(j.PDV.Count, j.PDV.P95),
			PdvRttP99:     optDuration(j.PDV.Count, j.PDV.P99),
			PdvRttP999:    optDuration(j.PDV.Count, j.PDV.P999),
			JitterRfc3550: optDuration(s.RTT.Count, j.RFC3550),
			RttStddev:     optDuration(s.RTT.Count, s.RTT.StdDev),
			JitterP95P50:  optDuration(s.RTT.Count, j.P95P50),
		},
		Hops: &gotwampv1.HopStats{
			FwdMin: hop(s.ForwardHops.Min), FwdMax: hop(s.ForwardHops.Max), Fwd: hop(s.ForwardHops.Mode),
			RevMin: hop(s.ReverseHops.Min), RevMax: hop(s.ReverseHops.Max), Rev: hop(s.ReverseHops.Mode),
		},
		Clock: &gotwampv1.ClockInfo{
			SenderSynced:     s.SenderSynced,
			ReflectorSynced:  s.ReflectorSynced,
			MaxClockError:    durationpb.New(s.MaxClockError),
			TimestampingMode: protoTimestamping(s.Timestamping),
			// A Multiplier of 0 makes the sum no bound (SPEC §8.4).
			MaxClockErrorUnknown: s.MaxClockErrorUnknown,
			TxTimestamps:         s.TxTimestamps,
		},
		Aborted:                    s.Aborted,
		Invalid:                    s.Invalid,
		Kind:                       kind,
		NegativeOwd:                s.NegativeOWD,
		InvalidRtt:                 s.InvalidRTT,
		TxTimestampMissing:         s.TxMissing,
		TxTimestampInvalid:         s.TxInvalid,
		DscpReflected:              dscp,
		UnsupportedTimestampFormat: s.Unsupported,
		ReflectedMismatch:          s.ReflectedMismatch,
	}
}

// delayStats converts a distribution; statistics of an empty one are unset.
func delayStats(d stats.Distribution) *gotwampv1.DelayStats {
	if d.Count == 0 {
		return &gotwampv1.DelayStats{}
	}
	return &gotwampv1.DelayStats{
		Min:     durationpb.New(d.Min),
		Mean:    durationpb.New(d.Mean),
		Median:  durationpb.New(d.Median),
		Max:     durationpb.New(d.Max),
		P95:     durationpb.New(d.P95),
		P99:     durationpb.New(d.P99),
		Stddev:  durationpb.New(d.StdDev),
		Samples: clamp32(d.Count),
	}
}

func optDuration(n uint64, d time.Duration) *durationpb.Duration {
	if n == 0 {
		return nil
	}
	return durationpb.New(d)
}

func clamp32(v uint64) uint32 { return uint32(min(v, math.MaxUint32)) }

// count32 converts a non-negative count.
//
//declscope:package // the reflector service reports counts too
func count32(n int) uint32 { return uint32(min(max(n, 0), math.MaxUint32)) } //nolint:gosec // G115: clamped

// optTimestamp converts t, leaving a zero time unset.
//
//declscope:package // the reflector service reports times too
func optTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// runError converts a run failure; stage is a client.Stage name.
//
//declscope:package // RunTest reports failures through it
func runError(stage, message string, accept int) *gotwampv1.TestEvent {
	st := gotwampv1.RunStage(gotwampv1.RunStage_value["RUN_STAGE_"+strings.ToUpper(stage)])
	return &gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Error{Error: &gotwampv1.RunError{
		Stage: st, Message: message, Accept: count32(accept),
	}}}
}

// storeEvent converts a result of a monitoring session for WatchResults.
// A summary reports the TestSpec of the session definition the event was
// published under (eventSpec). Skipped runs have no event and yield nil.
//
//declscope:package // WatchResults streams the store's events through it
func storeEvent(e store.Event) *gotwampv1.TestEvent {
	var ev *gotwampv1.TestEvent
	switch e.Kind {
	case store.EventRunSummary, store.EventWindowSummary:
		kind := gotwampv1.SummaryKind_SUMMARY_KIND_RUN
		if e.Kind == store.EventWindowSummary {
			kind = gotwampv1.SummaryKind_SUMMARY_KIND_WINDOW
		}
		ev = &gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Summary{Summary: runSummary(*e.Summary, e.Session.Name, e.RunID, eventSpec(e.Session), e.SID, kind)}}
	case store.EventRunError:
		ev = runError(e.Error.Stage, e.Error.Message, e.Error.Accept)
	case store.EventPacket:
		ev = packetEvent(e.Packet)
	case store.EventLost:
		ev = &gotwampv1.TestEvent{Event: &gotwampv1.TestEvent_Lost{Lost: &gotwampv1.PacketLost{Seq: e.Lost.Seq, T1: timestamppb.New(e.Lost.T1)}}}
	}
	if ev == nil {
		return nil
	}
	ev.Session, ev.RunId = e.Session.Name, e.RunID
	return ev
}

// eventSpec returns the TestSpec a session's result was measured with:
// that of the session definition it was published under, not the current
// one (Codex M4). It is built for each summary; summaries are few.
func eventSpec(info *store.SessionInfo) *gotwampv1.TestSpec {
	def, ok := info.Definition.(config.Session)
	if !ok {
		return nil
	}
	return testSpecProto(def.Target, def.IPV, def.Source, def.TestParams)
}

// protoTimestamping converts a socket's timestamping mode.
//
//declscope:package // the daemon service reports modes too
func protoTimestamping(m tstamp.Mode) gotwampv1.TimestampingMode {
	switch m {
	case tstamp.ModeKernel:
		return gotwampv1.TimestampingMode_TIMESTAMPING_MODE_KERNEL
	case tstamp.ModeUserspace:
		return gotwampv1.TimestampingMode_TIMESTAMPING_MODE_USERSPACE
	case tstamp.ModeHardware:
		return gotwampv1.TimestampingMode_TIMESTAMPING_MODE_HARDWARE
	default:
		return gotwampv1.TimestampingMode_TIMESTAMPING_MODE_UNSPECIFIED
	}
}

func clamp63(v uint64) int64 { return int64(min(v, math.MaxInt64)) }

// hop converts a hop count, of which negative means unknown.
func hop(v int) uint32 { return uint32(min(max(v, 0), 255)) } //nolint:gosec // G115: clamped to 0-255
