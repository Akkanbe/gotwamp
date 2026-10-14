package wire_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestModes(t *testing.T) {
	offered := wire.ModeUnauthenticated | wire.ModeReflectOctets | 1<<20
	if !offered.Has(wire.ModeUnauthenticated) || !offered.Has(wire.ModeUnauthenticated|wire.ModeReflectOctets) {
		t.Error("Has missed set bits")
	}
	if offered.Has(wire.ModeUnauthenticated | wire.ModeAuthenticated) {
		t.Error("Has accepted a missing bit")
	}

	security := []struct {
		m    wire.Modes
		want wire.SecurityMode
		ok   bool
	}{
		{0, 0, false},
		{wire.ModeUnauthenticated, wire.SecurityUnauthenticated, true},
		{wire.ModeAuthenticated | wire.ModeReflectOctets, wire.SecurityAuthenticated, true},
		{wire.ModeEncrypted | 1<<31, wire.SecurityEncrypted, true},
		{wire.ModeMixed, wire.SecurityMixed, true},
		{wire.ModeUnauthenticated | wire.ModeAuthenticated, 0, false},
		{wire.ModeSymmetricalSize, 0, false},
	}
	for _, tt := range security {
		got, ok := tt.m.Security()
		if got != tt.want || ok != tt.ok {
			t.Errorf("%v.Security() = %v, %v; want %v, %v", tt.m, got, ok, tt.want, tt.ok)
		}
	}

	names := []struct {
		m    wire.Modes
		want string
	}{
		{0, "0"},
		{wire.ModeUnauthenticated, "unauthenticated"},
		{0x71, "unauthenticated|individual-session-control|reflect-octets|symmetrical-size"},
		{wire.ModePTPv2Timestamp | wire.ModeDSCPECNMonitoring | wire.ModeIKEv2Derived, "ikev2-derived|dscp-ecn-monitoring|ptpv2-timestamp"},
		{wire.ModeEncrypted | 1<<10 | 1<<12, "encrypted|0x1400"},
	}
	for _, tt := range names {
		if got := tt.m.String(); got != tt.want {
			t.Errorf("Modes(%#x).String() = %q, want %q", uint32(tt.m), got, tt.want)
		}
	}
}

func TestSecurityMode(t *testing.T) {
	tests := []struct {
		s                 wire.SecurityMode
		valid             bool
		test              wire.SecurityMode
		sender, reflector int
		truncation        int
		name              string
	}{
		{wire.SecurityUnauthenticated, true, wire.SecurityUnauthenticated, 14, 41, 27, "unauthenticated"},
		{wire.SecurityAuthenticated, true, wire.SecurityAuthenticated, 48, 112, 64, "authenticated"},
		{wire.SecurityEncrypted, true, wire.SecurityEncrypted, 48, 112, 64, "encrypted"},
		{wire.SecurityMixed, true, wire.SecurityUnauthenticated, 14, 41, 27, "mixed"},
		{0, false, 0, 0, 0, 0, "SecurityMode(0)"},
		{3, false, 3, 0, 0, 0, "SecurityMode(unauthenticated|authenticated)"},
	}
	for _, tt := range tests {
		if tt.s.Valid() != tt.valid {
			t.Errorf("%v.Valid() = %v", tt.s, !tt.valid)
		}
		if tt.s.TestMode() != tt.test {
			t.Errorf("%v.TestMode() = %v, want %v", tt.s, tt.s.TestMode(), tt.test)
		}
		if tt.s.SenderHeaderSize() != tt.sender || tt.s.ReflectorHeaderSize() != tt.reflector || tt.s.PaddingTruncation() != tt.truncation {
			t.Errorf("%v sizes = %d/%d/%d, want %d/%d/%d", tt.s, tt.s.SenderHeaderSize(), tt.s.ReflectorHeaderSize(), tt.s.PaddingTruncation(), tt.sender, tt.reflector, tt.truncation)
		}
		if tt.s.String() != tt.name {
			t.Errorf("String() = %q, want %q", tt.s.String(), tt.name)
		}
		if tt.valid {
			if got, ok := tt.s.Mode().Security(); !ok || got != tt.s {
				t.Errorf("%v.Mode().Security() = %v, %v", tt.s, got, ok)
			}
		}
	}
	if wire.DefaultPaddingLength != 27 {
		t.Errorf("DefaultPaddingLength = %d, want 27", wire.DefaultPaddingLength)
	}
}

func TestAccept(t *testing.T) {
	tests := []struct {
		a    wire.Accept
		norm wire.Accept
		name string
	}{
		{wire.AcceptOK, wire.AcceptOK, "OK"},
		{wire.AcceptFailure, wire.AcceptFailure, "failure"},
		{wire.AcceptInternalError, wire.AcceptInternalError, "internal error"},
		{wire.AcceptNotSupported, wire.AcceptNotSupported, "not supported"},
		{wire.AcceptPermanentLimit, wire.AcceptPermanentLimit, "permanent resource limitation"},
		{wire.AcceptTemporaryLimit, wire.AcceptTemporaryLimit, "temporary resource limitation"},
		{6, wire.AcceptFailure, "Accept(6)"}, // RFC 7717 §5.3, read as 1 by others
		{255, wire.AcceptFailure, "Accept(255)"},
	}
	for _, tt := range tests {
		if got := tt.a.Normalize(); got != tt.norm {
			t.Errorf("Accept(%d).Normalize() = %d, want %d", tt.a, got, tt.norm)
		}
		if got := tt.a.String(); got != tt.name {
			t.Errorf("Accept(%d).String() = %q, want %q", tt.a, got, tt.name)
		}
	}
}

func TestCommand(t *testing.T) {
	tests := []struct {
		c    wire.Command
		size int
		name string
	}{
		{wire.CommandReserved, 0, "Reserved"},
		{wire.CommandForbidden, 0, "Forbidden"},
		{wire.CommandStartSessions, 32, "Start-Sessions"},
		{wire.CommandStopSessions, 32, "Stop-Sessions"},
		{wire.CommandFetchSession, 0, "Fetch-Session"},
		{wire.CommandRequestTWSession, 112, "Request-TW-Session"},
		{wire.CommandExperimentation, 0, "Experimentation"},
		{wire.CommandStartNSessions, 0, "Start-N-Sessions"},
		{wire.CommandStartNAck, 0, "Start-N-Ack"},
		{wire.CommandStopNSessions, 0, "Stop-N-Sessions"},
		{wire.CommandStopNAck, 0, "Stop-N-Ack"},
		{wire.CommandRequestTWMicroSessions, 0, "Request-TW-Micro-Sessions"},
		{12, 0, "Command(12)"},
		{255, 0, "Command(255)"},
	}
	for _, tt := range tests {
		if got := tt.c.MessageSize(); got != tt.size {
			t.Errorf("%v.MessageSize() = %d, want %d", tt.c, got, tt.size)
		}
		if got := tt.c.String(); got != tt.name {
			t.Errorf("Command(%d).String() = %q, want %q", tt.c, got, tt.name)
		}
	}
	if wire.ControlHeaderSize > wire.StartSessionsSize {
		t.Error("ControlHeaderSize exceeds the shortest client message")
	}
}

func TestUnexpectedCommand(t *testing.T) {
	decoders := []struct {
		name string
		size int
		dec  func([]byte) error
	}{
		{"Request-TW-Session", wire.RequestTWSessionSize, func(b []byte) error { return new(wire.RequestTWSession).Decode(b) }},
		{"Start-Sessions", wire.StartSessionsSize, func(b []byte) error { return new(wire.StartSessions).Decode(b) }},
		{"Stop-Sessions", wire.StopSessionsSize, func(b []byte) error { return new(wire.StopSessions).Decode(b) }},
	}
	for _, d := range decoders {
		for _, c := range []wire.Command{wire.CommandReserved, wire.CommandForbidden, wire.CommandFetchSession, wire.CommandStartNSessions, 200} {
			b := make([]byte, d.size)
			b[0] = byte(c)
			if err := d.dec(b); !errors.Is(err, wire.ErrUnexpectedCommand) {
				t.Errorf("%s with command %v: error = %v, want ErrUnexpectedCommand", d.name, c, err)
			}
		}
	}
}

// TestRequestIPv4Address checks CMP-19: whatever an IPv4 address array holds
// past its first 4 octets, the encoding carries zeros there.
func TestRequestIPv4Address(t *testing.T) {
	r := wire.RequestTWSession{
		IPVN:            wire.IPVN4,
		SenderAddress:   [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 0, 2, 1}, // IPv4-mapped, not normalized
		ReceiverAddress: [16]byte{198, 51, 100, 2, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9},
	}
	b := r.Append(nil)
	if !bytes.Equal(b[16:32], make([]byte, 16)) {
		t.Errorf("sender address = %x, want the first 4 octets of the array and zeros", b[16:32])
	}
	if !bytes.Equal(b[32:48], []byte{198, 51, 100, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Errorf("receiver address = %x", b[32:48])
	}
	// IPVN high nibble is MBZ.
	r.IPVN = 0xf6
	if b := r.Append(nil); b[1] != 6 {
		t.Errorf("octet 1 = %#x, want 0x06", b[1])
	}
}

func TestTypeP(t *testing.T) {
	tests := []struct {
		t    wire.TypeP
		dscp uint8
		ok   bool
	}{
		{0, 0, true},
		{wire.TypePFromDSCP(46), 46, true},
		{wire.TypePFromDSCP(63), 63, true},
		{wire.TypePFromDSCP(0xff), 63, true},
		{0x2e000001, 46, false},
		{0x40000000, 0, false}, // OWAMP PHB-ID form
		{0x80000000, 0, false},
	}
	for _, tt := range tests {
		dscp, ok := tt.t.DSCP()
		if dscp != tt.dscp || ok != tt.ok {
			t.Errorf("TypeP(%#08x).DSCP() = %d, %v; want %d, %v", uint32(tt.t), dscp, ok, tt.dscp, tt.ok)
		}
	}
	if wire.TypePFromDSCP(46) != 0x2e000000 {
		t.Errorf("TypePFromDSCP(46) = %#x", uint32(wire.TypePFromDSCP(46)))
	}
}

func TestConstructorsReadRandom(t *testing.T) {
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 64))
	g, err := wire.NewServerGreeting(wire.ModeUnauthenticated, wire.GreetingCountDefault, rnd)
	if err != nil {
		t.Fatal(err)
	}
	if g.Challenge != [16]byte(bytes.Repeat([]byte{0x5a}, 16)) || g.Salt != g.Challenge || g.Count != 2048 || g.Modes != wire.ModeUnauthenticated {
		t.Errorf("NewServerGreeting = %+v", g)
	}

	s, err := wire.NewServerStart(wire.AcceptOK, ntptime.New(1, 2), rnd)
	if err != nil {
		t.Fatal(err)
	}
	if s.ServerIV != [16]byte(bytes.Repeat([]byte{0x5a}, 16)) || s.StartTime != ntptime.New(1, 2) {
		t.Errorf("NewServerStart = %+v", s)
	}
	s, err = wire.NewServerStart(wire.AcceptFailure, ntptime.New(1, 2), rnd)
	if err != nil {
		t.Fatal(err)
	}
	if !s.StartTime.IsZero() || s.Accept != wire.AcceptFailure {
		t.Errorf("rejecting NewServerStart kept Start-Time: %+v", s)
	}

	sid, err := wire.NewSID([4]byte{192, 0, 2, 1}, ntptime.New(0xee6c1580, 0x80000000), bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "c0000201ee6c158080000000deadbeef"; sid.String() != want {
		t.Errorf("NewSID = %v, want %s", sid, want)
	}
	if sid.IsZero() || !(wire.SID{}).IsZero() {
		t.Error("SID.IsZero wrong")
	}
}

func TestConstructorsRandomFailure(t *testing.T) {
	for _, n := range []int{0, 16, 31} {
		if _, err := wire.NewServerGreeting(1, 2048, bytes.NewReader(make([]byte, n))); !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			t.Errorf("NewServerGreeting with %d random octets: err = %v", n, err)
		}
	}
	if _, err := wire.NewServerStart(0, 0, bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("NewServerStart: err = %v", err)
	}
	if _, err := wire.NewSID([4]byte{}, 0, bytes.NewReader([]byte{1, 2})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("NewSID: err = %v", err)
	}
}
