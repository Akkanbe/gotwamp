package daemon

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
)

func validTest() *gotwampv1.TestSpec {
	return &gotwampv1.TestSpec{Target: "192.0.2.10", Mode: gotwampv1.Mode_MODE_LIGHT, Count: proto.Uint32(5)}
}

// TestCheckTestRequest covers every field of RunTest that has a range.
func TestCheckTestRequest(t *testing.T) {
	cfg := config.Default()
	mod := func(f func(*gotwampv1.TestSpec)) *gotwampv1.TestSpec {
		s := validTest()
		f(s)
		return s
	}
	cases := []struct {
		name string
		spec *gotwampv1.TestSpec
		want string // substring of the message
	}{
		{"nil", nil, "spec is required"},
		{"target empty", mod(func(s *gotwampv1.TestSpec) { s.Target = "" }), "target: is required"},
		{"target syntax", mod(func(s *gotwampv1.TestSpec) { s.Target = "a b" }), "neither an IP address nor a host name"},
		{"target too long", mod(func(s *gotwampv1.TestSpec) { s.Target = strings.Repeat("a", 300) }), "neither"},
		{"mode", mod(func(s *gotwampv1.TestSpec) { s.Mode = 9 }), "mode: unknown value 9"},
		{"ipv", mod(func(s *gotwampv1.TestSpec) { s.Ipv = 7 }), "ipv: unknown value 7"},
		{"padding fill", mod(func(s *gotwampv1.TestSpec) { s.PaddingFill = 8 }), "padding_fill: unknown"},
		{"security unknown", mod(func(s *gotwampv1.TestSpec) { s.Security = 77 }), "security: unknown"},
		{"start time", mod(func(s *gotwampv1.TestSpec) { s.Compat = &gotwampv1.CompatOptions{StartTime: 5} }), "start_time: unknown"},
		{"source", mod(func(s *gotwampv1.TestSpec) { s.SourceAddress = "nope" }), "source"},
		{"control port", mod(func(s *gotwampv1.TestSpec) { s.ControlPort = 70000 }), "control_port"},
		{"receiver port", mod(func(s *gotwampv1.TestSpec) { s.ReceiverPort = proto.Uint32(65536) }), "receiver_port"},
		{"sender port", mod(func(s *gotwampv1.TestSpec) { s.SenderPort = proto.Uint32(1 << 31) }), "sender_port"},
		{"port range", mod(func(s *gotwampv1.TestSpec) {
			s.Compat = &gotwampv1.CompatOptions{SenderPortRange: &gotwampv1.PortRange{First: 70000, Last: 70001}}
		}), "sender_port_range"},
		{"count 0", mod(func(s *gotwampv1.TestSpec) { s.Count = proto.Uint32(0) }), "count"},
		{"count over RunTest", mod(func(s *gotwampv1.TestSpec) { s.Count = proto.Uint32(100_001) }), "exceeds 100000"},
		{"count over schema", mod(func(s *gotwampv1.TestSpec) { s.Count = proto.Uint32(1 << 31) }), "count"},
		{"interval short", mod(func(s *gotwampv1.TestSpec) { s.Interval = durationpb.New(time.Microsecond) }), "interval"},
		{"interval long", mod(func(s *gotwampv1.TestSpec) { s.Interval = durationpb.New(2 * time.Hour) }), "interval"},
		{"interval malformed", mod(func(s *gotwampv1.TestSpec) { s.Interval = &durationpb.Duration{Seconds: 1, Nanos: -5} }), "interval:"},
		{"timeout zero", mod(func(s *gotwampv1.TestSpec) { s.Timeout = durationpb.New(0) }), "timeout"},
		{"timeout long", mod(func(s *gotwampv1.TestSpec) { s.Timeout = durationpb.New(2 * time.Hour) }), "timeout"},
		{"timeout malformed", mod(func(s *gotwampv1.TestSpec) { s.Timeout = &durationpb.Duration{Seconds: 1 << 62} }), "timeout:"},
		{"control timeout", mod(func(s *gotwampv1.TestSpec) { s.ControlTimeout = durationpb.New(-time.Second) }), "control_timeout"},
		{"end delay", mod(func(s *gotwampv1.TestSpec) {
			s.Compat = &gotwampv1.CompatOptions{EndDelay: durationpb.New(3 * time.Hour)}
		}), "end_delay"},
		{"padding", mod(func(s *gotwampv1.TestSpec) { s.Padding = proto.Uint32(9001) }), "padding"},
		{"dscp", mod(func(s *gotwampv1.TestSpec) { s.Dscp = proto.Uint32(64) }), "dscp"},
		{"control dscp", mod(func(s *gotwampv1.TestSpec) { s.Compat = &gotwampv1.CompatOptions{ControlDscp: proto.Uint32(99)} }), "dscp"},
		{"ttl", mod(func(s *gotwampv1.TestSpec) { s.Ttl = 256 }), "ttl"},
	}
	for _, tc := range cases {
		_, _, err := checkTestRequest(cfg, nil, tc.spec)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want InvalidArgument with %q", tc.name, err, tc.want)
		}
	}
	if _, _, err := checkTestRequest(cfg, nil, validTest()); err != nil {
		t.Errorf("valid request: %v", err)
	}
}

func TestCheckSessionRequest(t *testing.T) {
	cfg := config.Default()
	ok := func() *gotwampv1.SessionSpec {
		return &gotwampv1.SessionSpec{Name: "s1", Spec: validTest(), Schedule: &gotwampv1.Schedule{Frequency: durationpb.New(time.Minute)}}
	}
	mod := func(f func(*gotwampv1.SessionSpec)) *gotwampv1.SessionSpec {
		s := ok()
		f(s)
		return s
	}
	cases := []struct {
		name string
		spec *gotwampv1.SessionSpec
		want string
	}{
		{"nil", nil, "required"},
		{"no test", &gotwampv1.SessionSpec{Name: "a"}, "required"},
		{"name", mod(func(s *gotwampv1.SessionSpec) { s.Name = "Bad/Name" }), "session.name"},
		{"name long", mod(func(s *gotwampv1.SessionSpec) { s.Name = strings.Repeat("a", 64) }), "session.name"},
		{"schedule type", mod(func(s *gotwampv1.SessionSpec) { s.Schedule.Type = 9 }), "schedule.type: unknown"},
		{"frequency malformed", mod(func(s *gotwampv1.SessionSpec) { s.Schedule.Frequency = &durationpb.Duration{Seconds: -1, Nanos: 1} }), "schedule.frequency:"},
		{"frequency long", mod(func(s *gotwampv1.SessionSpec) { s.Schedule.Frequency = durationpb.New(48 * time.Hour) }), "frequency"},
		{"frequency short", mod(func(s *gotwampv1.SessionSpec) { s.Schedule.Frequency = durationpb.New(time.Second) }), "frequency"},
		{"window", mod(func(s *gotwampv1.SessionSpec) {
			s.Schedule = &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS, Window: durationpb.New(time.Millisecond)}
		}), "window"},
		{"labels", mod(func(s *gotwampv1.SessionSpec) { s.Labels = map[string]string{"session": "x"} }), "reserved"},
		{"label value", mod(func(s *gotwampv1.SessionSpec) { s.Labels = map[string]string{"site": "\xff"} }), "UTF-8"},
		{"test field", mod(func(s *gotwampv1.SessionSpec) { s.Spec.Ttl = 300 }), "ttl"},
	}
	for _, tc := range cases {
		_, err := checkSessionRequest(cfg, nil, tc.spec)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want InvalidArgument with %q", tc.name, err, tc.want)
		}
	}
	if _, err := checkSessionRequest(cfg, nil, ok()); err != nil {
		t.Errorf("valid request: %v", err)
	}
}

func TestCheckSmallRequests(t *testing.T) {
	for _, err := range []error{
		checkName(""), checkName("UPPER"), checkName(strings.Repeat("x", 64)),
		checkRunID(""), checkRunID(strings.Repeat("x", 65)),
		checkWatch(&gotwampv1.WatchResultsRequest{Sessions: []string{"ok", "Not OK"}}),
		checkWatch(&gotwampv1.WatchResultsRequest{Sessions: make([]string, maxWatchNames+1)}),
		checkSince(&timestamppb.Timestamp{Seconds: 1 << 62}),
		checkReflectorKind(9),
	} {
		if status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v, want InvalidArgument", err)
		}
	}
	for _, err := range []error{
		checkName("tokyo-osaka_1"), checkRunID("01J0000000000000000000000"), checkWatch(&gotwampv1.WatchResultsRequest{}),
		checkSince(nil), checkSince(timestamppb.Now()), checkReflectorKind(gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL),
	} {
		if err != nil {
			t.Errorf("valid input: %v", err)
		}
	}
}

// FuzzTestRequest: no RunTest request makes validation panic, and every
// accepted one plans a run inside the limits of SPEC §9.5.
func FuzzTestRequest(f *testing.F) {
	cfg := config.Default()
	for _, s := range []*gotwampv1.TestSpec{validTest(), {Target: "x", Count: proto.Uint32(0)}, {Target: "::1", Interval: &durationpb.Duration{Seconds: -3}}} {
		b, _ := proto.Marshal(s)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var s gotwampv1.TestSpec
		if proto.Unmarshal(b, &s) != nil {
			return
		}
		plan, params, err := checkTestRequest(cfg, nil, &s)
		if err != nil {
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("error %v is not InvalidArgument", err)
			}
			return
		}
		if params.Count < 1 || params.Count > maxRunTestCount || plan.test.Interval < time.Millisecond ||
			plan.test.Interval > time.Hour || plan.test.Timeout <= 0 || plan.test.Padding > 9000 || plan.test.DSCP > 63 ||
			plan.test.TTL < 1 || plan.test.TTL > 255 {
			t.Fatalf("accepted out-of-range request: %+v", params)
		}
	})
}

// FuzzSessionRequest: no session definition makes validation panic.
func FuzzSessionRequest(f *testing.F) {
	cfg := config.Default()
	b, _ := proto.Marshal(&gotwampv1.SessionSpec{Name: "s", Spec: validTest(), Schedule: &gotwampv1.Schedule{Frequency: durationpb.New(time.Minute)}})
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		var s gotwampv1.SessionSpec
		if proto.Unmarshal(b, &s) != nil {
			return
		}
		if _, err := checkSessionRequest(cfg, nil, &s); err != nil && status.Code(err) != codes.InvalidArgument {
			t.Fatalf("error %v is not InvalidArgument", err)
		}
	})
}
