package gotwamp_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp"
	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/stats"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestPublicValuesPreserveIdentity(t *testing.T) {
	var sid gotwamp.SID
	if err := sid.UnmarshalText([]byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(sid)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"0123456789abcdef0123456789abcdef"` {
		t.Fatalf("SID JSON = %s", b)
	}
	wireSID, summary := publicAliases(sid, stats.Summary{RTT: stats.Distribution{Count: 1, Mean: time.Millisecond}})
	if wireSID.String() != sid.String() {
		t.Fatal("SID alias lost value")
	}
	if summary.RTT.Mean != time.Millisecond {
		t.Fatal("summary alias lost value")
	}
	err = &client.Error{Stage: client.StageRequest, Accept: wire.AcceptTemporaryLimit, Err: client.ErrRejected}
	var control *gotwamp.ControlError
	if !errors.As(err, &control) || !errors.Is(err, gotwamp.ErrRejected) {
		t.Fatalf("public error identity: %v", err)
	}
	if control.Stage != gotwamp.StageRequest || control.Accept != gotwamp.AcceptTemporaryLimit {
		t.Fatalf("public control error = %+v", control)
	}
}

func TestControlValidationIsPure(t *testing.T) {
	tests := []struct {
		name    string
		cfg     client.Config
		wantErr bool
	}{
		{name: "defaults"},
		{name: "invalid security bits", cfg: client.Config{Modes: gotwamp.ModeReflectOctets}, wantErr: true},
		{name: "unsupported feature", cfg: client.Config{Features: gotwamp.ModePTPv2Timestamp}, wantErr: true},
		{name: "missing credentials", cfg: client.Config{Modes: gotwamp.ModeEncrypted}, wantErr: true},
		{name: "authenticated", cfg: client.Config{Modes: gotwamp.ModeAuthenticated, KeyID: "test", Passphrase: []byte("secret")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modes := tt.cfg.Modes
			if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() = %v", err)
			}
			if tt.cfg.Modes != modes {
				t.Fatal("Validate changed configuration")
			}
		})
	}
}

func TestCredentials(t *testing.T) {
	var store gotwamp.CredentialStore = gotwamp.Credentials{"test": []byte("secret")}
	if secret, ok := store.Passphrase("test"); !ok || string(secret) != "secret" {
		t.Fatal("credential lookup failed")
	}
	if _, ok := store.Passphrase("missing"); ok {
		t.Fatal("unknown identity accepted")
	}
}

// publicAliases requires assignment-compatible canonical types, without conversions.
func publicAliases(sid gotwamp.SID, summary stats.Summary) (wire.SID, gotwamp.Summary) {
	return sid, summary
}
