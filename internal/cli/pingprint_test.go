package cli

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

func TestStartLineAndDSCPNames(t *testing.T) {
	full := &gotwampv1.RunStarted{
		Spec:   &gotwampv1.TestSpec{Target: "2001:db8::10", Mode: gotwampv1.Mode_MODE_FULL, ControlPort: 862, Count: proto.Uint32(100), Interval: durationpb.New(100 * time.Millisecond), Padding: proto.Uint32(27), Dscp: proto.Uint32(34)},
		Local:  &gotwampv1.Endpoint{Address: "2001:db8::1", Port: 50000},
		Remote: &gotwampv1.Endpoint{Address: "2001:db8::10", Port: 51234},
	}
	if got, want := startLine(full), "TWAMP full [2001:db8::10]:862 -> reflector [2001:db8::10]:51234, 100 packets, interval 100ms, padding 27, dscp af41"; got != want {
		t.Errorf("startLine = %q\nwant %q", got, want)
	}
	for v, want := range map[uint32]string{0: "0", 8: "cs1", 46: "ef", 44: "va", 10: "af11", 38: "af43", 56: "cs7", 1: "1", 63: "63", 40: "cs5"} {
		if got := dscpName(v); got != want {
			t.Errorf("dscpName(%d) = %q, want %q", v, got, want)
		}
	}
}
