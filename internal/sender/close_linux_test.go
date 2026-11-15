//go:build linux

package sender_test

import (
	"net/netip"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

func TestCloseIsIdempotent(t *testing.T) {
	cfg := sender.DefaultConfig(netip.MustParseAddrPort("127.0.0.1:862"))
	cfg.Timestamping = tstamp.ModeUserspace
	s, err := sender.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}
