package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestRunTestExtensions: an ad-hoc Full test with every TWAMP extension
// against the daemon's own control server, which offers them all by
// default (RFC 5938, RFC 6038).
func TestRunTestExtensions(t *testing.T) {
	r := start(t, testConfig(t), Options{})
	defer r.stop(t)
	exts := []gotwampv1.Extension{
		gotwampv1.Extension_EXTENSION_INDIVIDUAL_SESSION_CONTROL, gotwampv1.Extension_EXTENSION_REFLECT_OCTETS,
		gotwampv1.Extension_EXTENSION_SYMMETRICAL_SIZE,
	}
	spec := &gotwampv1.TestSpec{
		Target: "127.0.0.1", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: uint32(r.d.ControlAddrs()[0].Port()),
		Count: proto.Uint32(3), Interval: durationpb.New(10 * time.Millisecond), Timeout: durationpb.New(time.Second),
		Padding: proto.Uint32(100), Extensions: exts, OctetsToReflect: proto.Uint32(0xbeef), PaddingToReflect: proto.Uint32(16),
	}
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	sum := evs[len(evs)-1].GetSummary()
	if sum.GetReceived() != 3 || sum.GetReflectedMismatch() != 0 || !slices.Equal(sum.GetSpec().GetExtensions(), exts) ||
		sum.GetSpec().GetOctetsToReflect() != 0xbeef || sum.GetSpec().GetPaddingToReflect() != 16 {
		t.Fatalf("summary = %v (events %v)", sum, evs)
	}
}

// TestRunTestSTAMP: a STAMP Light test against the daemon's Light
// reflector in STAMP mode (RFC 8762, RFC 8972).
func TestRunTestSTAMP(t *testing.T) {
	c := testConfig(t)
	c.Reflector.Light.STAMP = true
	r := start(t, c, Options{})
	defer r.stop(t)
	spec := lightSpec(r.d.LightAddrs()[0], 3)
	spec.Stamp, spec.Ssid = proto.Bool(true), proto.Uint32(7)
	stream, err := gotwampv1.NewSenderServiceClient(r.conn).RunTest(t.Context(), &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, stream)
	if sum := evs[len(evs)-1].GetSummary(); sum.GetReceived() != 3 || !sum.GetSpec().GetStamp() || sum.GetSpec().GetSsid() != 7 {
		t.Fatalf("summary = %v", sum)
	}
}

// TestExtensionRequests: the extensions belong to Full, STAMP to Light.
func TestExtensionRequests(t *testing.T) {
	cfg := config.Default()
	cfg.Sender.Enabled = true
	for name, spec := range map[string]*gotwampv1.TestSpec{
		"light with an extension": {Target: "x", Mode: gotwampv1.Mode_MODE_LIGHT, Extensions: []gotwampv1.Extension{gotwampv1.Extension_EXTENSION_SYMMETRICAL_SIZE}},
		"full with stamp":         {Target: "x", Mode: gotwampv1.Mode_MODE_FULL, Stamp: proto.Bool(true)},
		"unknown extension":       {Target: "x", Mode: gotwampv1.Mode_MODE_FULL, Extensions: []gotwampv1.Extension{9}},
		"ssid without stamp":      {Target: "x", Mode: gotwampv1.Mode_MODE_LIGHT, Ssid: proto.Uint32(3)},
	} {
		if _, _, err := checkTestRequest(cfg, nil, spec); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
	// The defaults' extensions do not reach a Light test.
	cfg.Sender.Defaults.Extensions = []string{"reflect_octets"}
	plan, params, err := checkTestRequest(cfg, nil, &gotwampv1.TestSpec{Target: "x", Mode: gotwampv1.Mode_MODE_LIGHT})
	if err != nil || len(params.Extensions) != 0 || plan.fc.Features != 0 {
		t.Errorf("light inheriting extensions: %v, %+v", err, params)
	}
	// Either Reflect Octets field asks for the extension: the echo value
	// and the length of padding to reflect.
	plan, _, err = checkTestRequest(cfg, nil, &gotwampv1.TestSpec{Target: "x", Mode: gotwampv1.Mode_MODE_FULL, OctetsToReflect: proto.Uint32(4)})
	if err != nil || plan.fc.Features != wire.ModeReflectOctets || plan.fc.OctetsToReflect != 4 {
		t.Errorf("octets to reflect: %v, features %v", err, plan.fc.Features)
	}
	plan, _, err = checkTestRequest(cfg, nil, &gotwampv1.TestSpec{Target: "x", Mode: gotwampv1.Mode_MODE_FULL, PaddingToReflect: proto.Uint32(8), Padding: proto.Uint32(64)})
	if err != nil || plan.fc.Features != wire.ModeReflectOctets || plan.fc.PaddingToReflect != 8 || plan.fc.OctetsToReflect != 0 {
		t.Errorf("padding to reflect: %v, features %v", err, plan.fc.Features)
	}
	if m := featureModes([]string{"individual_session_control", "symmetrical_size"}, true); m != wire.ModeIndividualSessionControl|wire.ModeSymmetricalSize|wire.ModeReflectOctets {
		t.Errorf("featureModes = %v", m)
	}
	if !strings.Contains(config.Default().Reflector.Control.Extensions[0], "individual") {
		t.Error("the control server does not offer the extensions by default")
	}
}
