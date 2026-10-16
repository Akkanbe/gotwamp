// Package consumer_test intentionally lives in a separate module, outside
// gotwamp's internal import boundary.
package consumer_test

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/reflector"
	"github.com/Akkanbe/gotwamp/sender"
)

func TestPublicSenderContracts(t *testing.T) {
	target := netip.MustParseAddrPort("127.0.0.1:862")
	cfg := sender.DefaultConfig(target)
	cfg.Timestamping = gotwamp.TimestampUserspace
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	full := sender.FullConfig{
		Server: target, Modes: gotwamp.ModeEncrypted,
		KeyID: "test", Passphrase: []byte("synthetic-test-secret"),
		Features: gotwamp.ModeIndividualSessionControl | gotwamp.ModeSymmetricalSize,
		Accepted: func(id gotwamp.SID, peer netip.AddrPort) { _, _ = id.String(), peer },
	}
	if err := sender.ValidateFull(cfg, full); err != nil {
		t.Fatal(err)
	}
	full.Passphrase = nil
	if err := sender.ValidateFull(cfg, full); err == nil {
		t.Fatal("missing credentials accepted")
	}
	var packet gotwamp.PacketRecord
	var result gotwamp.Result
	var estimate gotwamp.ErrorEstimate
	var distribution gotwamp.Distribution
	var aggregate gotwamp.Summary
	hooks := sender.Hooks{
		Packet:  func(p sender.PacketResult) { packet, result, estimate = p.Record, p.Result, p.ReflectorErrorEstimate },
		Summary: func(s sender.Summary) { aggregate, distribution = s.Summary, s.RTT },
	}
	_ = []sender.Option{sender.WithHooks(hooks), sender.WithClock(func() (bool, time.Duration) { return false, 0 })}
	_, _, _, _, _ = packet, result, estimate, distribution, aggregate
	var failure *gotwamp.ControlError
	err := &gotwamp.ControlError{Stage: gotwamp.StageRequest, Accept: gotwamp.AcceptNotSupported, Err: gotwamp.ErrRejected}
	if !errors.As(err, &failure) || !errors.Is(err, gotwamp.ErrRejected) {
		t.Fatal("public control error identity")
	}
}

func TestEngineDependencyBoundary(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "github.com/Akkanbe/gotwamp/sender", "github.com/Akkanbe/gotwamp/reflector")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/internal/daemon", "/internal/cli", "/internal/config", "/internal/schedule", "/internal/store", "/internal/export", "/internal/api", "google.golang.org/", "github.com/prometheus/", "yaml", "sqlite"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("engine dependency includes %s", forbidden)
		}
	}
}

// Compile all lifecycle entry points without starting network activity.
var (
	_ func(context.Context, sender.Config, ...sender.Option) (sender.Summary, error)                    = sender.RunLight
	_ func(context.Context, sender.Config, sender.FullConfig, ...sender.Option) (sender.Summary, error) = sender.RunFull
	_ gotwamp.CredentialStore                                                                           = gotwamp.Credentials{"test": []byte("synthetic-test-secret")}
	_ reflector.Config
	_ reflector.FullConfig
)
