package daemon

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/schedule"
)

// Request limits besides those of the configuration schema (SPEC §9.5,
// §12.3).
const (
	maxRunTestCount = 100_000
	maxRunIDLength  = 64
	maxWatchNames   = 1024
)

// invalid is an InvalidArgument status.
func invalid(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

// knownEnum reports whether e is one of the values its enum defines.
func knownEnum(e protoreflect.Enum) bool {
	return e.Descriptor().Values().ByNumber(e.Number()) != nil
}

// checkDuration rejects a malformed Duration (out of range, or seconds and
// nanos of different signs); nil is fine.
func checkDuration(field string, d *durationpb.Duration) error {
	if d == nil {
		return nil
	}
	if err := d.CheckValid(); err != nil {
		return invalid("%s: %v", field, err)
	}
	return nil
}

// checkTestSpecShape checks what the configuration schema cannot see: the
// enum values and the encoding of the durations of a TestSpec.
func checkTestSpecShape(s *gotwampv1.TestSpec) error {
	enums := []struct {
		field string
		e     protoreflect.Enum
	}{
		{"mode", s.GetMode()},
		{"ipv", s.GetIpv()},
		{"padding_fill", s.GetPaddingFill()},
		{"security", s.GetSecurity()},
		{"compat.start_time", s.GetCompat().GetStartTime()},
		{"compat.reflector_sequence", s.GetCompat().GetReflectorSequence()},
	}
	for _, e := range enums {
		if !knownEnum(e.e) {
			return invalid("%s: unknown value %d", e.field, e.e.Number())
		}
	}
	// A Light test ignores the mode it would inherit from sender.defaults,
	// but one the request asks for is a mistake (RFC 5357 Appendix I).
	if s.GetMode() == gotwampv1.Mode_MODE_LIGHT && s.GetSecurity() > gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED {
		return invalid("security: TWAMP Light is unauthenticated only")
	}
	for _, e := range s.GetExtensions() {
		if _, ok := extensionName(e); !ok {
			return invalid("extensions: unknown value %d", e)
		}
	}
	durations := []struct {
		field string
		d     *durationpb.Duration
	}{
		{"interval", s.GetInterval()},
		{"timeout", s.GetTimeout()},
		{"control_timeout", s.GetControlTimeout()},
		{"compat.end_delay", s.GetCompat().GetEndDelay()},
	}
	for _, d := range durations {
		if err := checkDuration(d.field, d.d); err != nil {
			return err
		}
	}
	return nil
}

// checkTestRequest validates an ad-hoc test request against the daemon's
// configuration and plans it, without touching the network: the target is
// resolved afterwards. Every failure is an InvalidArgument status.
//
//declscope:package // RunTest validates requests with it
func checkTestRequest(cfg *config.Config, keys keystore.Store, spec *gotwampv1.TestSpec) (testPlan, config.TestParams, error) {
	if spec == nil {
		return testPlan{}, config.TestParams{}, invalid("spec is required")
	}
	if err := checkTestSpecShape(spec); err != nil {
		return testPlan{}, config.TestParams{}, err
	}
	params := cfg.Sender.Defaults
	if err := overlayTest(&params, spec); err != nil {
		return testPlan{}, config.TestParams{}, invalid("%v", err)
	}
	params.DropInherited(cfg.Sender.Defaults)
	target, ipv, source := spec.GetTarget(), ipvName(spec.GetIpv()), spec.GetSourceAddress()
	if err := config.ValidateTest(target, ipv, source, &params); err != nil {
		return testPlan{}, config.TestParams{}, invalid("%v", err)
	}
	if params.Count > maxRunTestCount {
		return testPlan{}, config.TestParams{}, invalid("count %d exceeds %d (RunTest cannot run continuously)", params.Count, maxRunTestCount)
	}
	plan, err := newPlan(target, ipv, source, params, cfg.Daemon, keys)
	if err != nil {
		return testPlan{}, config.TestParams{}, invalid("%v", err)
	}
	return plan, params, nil
}

// checkSessionRequest validates a session definition of the API against
// the daemon's configuration and builds its scheduler definition.
//
//declscope:package // CreateSession and UpdateSession validate requests with it
func checkSessionRequest(cfg *config.Config, keys keystore.Store, ps *gotwampv1.SessionSpec) (schedule.SessionSpec, error) {
	if ps == nil || ps.GetSpec() == nil {
		return schedule.SessionSpec{}, invalid("spec and spec.spec are required")
	}
	if err := checkTestSpecShape(ps.GetSpec()); err != nil {
		return schedule.SessionSpec{}, err
	}
	sc := ps.GetSchedule()
	if !knownEnum(sc.GetType()) {
		return schedule.SessionSpec{}, invalid("schedule.type: unknown value %d", sc.GetType())
	}
	for field, d := range map[string]*durationpb.Duration{
		"schedule.frequency": sc.GetFrequency(), "schedule.jitter": sc.GetJitter(), "schedule.window": sc.GetWindow(),
	} {
		if err := checkDuration(field, d); err != nil {
			return schedule.SessionSpec{}, err
		}
	}
	def, err := sessionFromProto(cfg.Sender.Defaults, ps)
	if err != nil {
		return schedule.SessionSpec{}, invalid("%v", err)
	}
	if err := config.ValidateSession(&def); err != nil {
		return schedule.SessionSpec{}, invalid("%v", err)
	}
	spec, err := sessionSpec(def, cfg.Daemon, keys)
	if err != nil {
		return schedule.SessionSpec{}, invalid("%v", err)
	}
	return spec, nil
}

// checkName validates the session name of a request.
//
//declscope:package // every session RPC checks its name
func checkName(name string) error {
	if !config.ValidName(name) {
		return invalid("session name %q does not match [a-z0-9][a-z0-9_-]{0,62}", name)
	}
	return nil
}

// checkRunID validates the run ID of StopTest.
//
//declscope:package // StopTest checks its run ID
func checkRunID(id string) error {
	if id == "" || len(id) > maxRunIDLength {
		return invalid("run_id must be 1-%d characters", maxRunIDLength)
	}
	return nil
}

// checkWatch validates a WatchResults request.
//
//declscope:package // WatchResults checks its request
func checkWatch(req *gotwampv1.WatchResultsRequest) error {
	if n := len(req.GetSessions()); n > maxWatchNames {
		return invalid("%d sessions exceed the limit of %d", n, maxWatchNames)
	}
	for _, name := range req.GetSessions() {
		if err := checkName(name); err != nil {
			return err
		}
	}
	return nil
}

// checkSince validates the since field of GetSessionStats.
//
//declscope:package // GetSessionStats checks its request
func checkSince(t *timestamppb.Timestamp) error {
	if t == nil {
		return nil
	}
	if err := t.CheckValid(); err != nil {
		return invalid("since: %v", err)
	}
	return nil
}

// checkReflectorKind validates the kind of ListReflectorSessions.
//
//declscope:package // ListReflectorSessions checks its request
func checkReflectorKind(k gotwampv1.ReflectorKind) error {
	if !knownEnum(k) {
		return invalid("kind: unknown value %d", k)
	}
	return nil
}
