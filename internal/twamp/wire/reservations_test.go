package wire_test

import (
	"bytes"
	"testing"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestUnauthenticatedIsIdentity(t *testing.T) {
	var sec wire.ControlSecurity = wire.Unauthenticated{}
	msg := (&wire.StopSessions{Sessions: 1}).Append(nil)
	orig := bytes.Clone(msg)
	if err := sec.Seal(msg, true); err != nil || !bytes.Equal(msg, orig) {
		t.Errorf("Seal changed the message: %x, %v", msg, err)
	}
	if err := sec.Open(msg); err != nil || !bytes.Equal(msg, orig) {
		t.Errorf("Open changed the message: %x, %v", msg, err)
	}
	// Unauthenticated mode does not check the HMAC field (RFC 4656 §3.1).
	msg[len(msg)-1] = 0xff
	if err := sec.Verify(msg); err != nil {
		t.Errorf("Verify = %v", err)
	}
}

func TestPaddingLayout(t *testing.T) {
	padding := make([]byte, 100)
	for i := range padding {
		padding[i] = byte(i)
	}
	tests := []struct {
		name             string
		layout           wire.PaddingLayout
		pad              int
		reflected, extra int
		header, authHdr  int
		least, authLeast uint32 // least valid Padding Length, 0 for any
	}{
		{"RFC 5357 plain", wire.PaddingLayout{}, 100, 0, 100, 14, 48, 0, 0},
		{"reflect octets", wire.PaddingLayout{ReflectLength: 8}, 100, 8, 92, 14, 48, 35, 72},
		{"symmetrical size", wire.PaddingLayout{SymmetricalSize: true}, 100, 0, 100, 41, 112, 0, 0},
		{"both", wire.PaddingLayout{SymmetricalSize: true, ReflectLength: 8}, 100, 8, 92, 41, 112, 9, 9},
		{"padding shorter than the parts", wire.PaddingLayout{ReflectLength: 8}, 3, 3, 0, 14, 48, 35, 72},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reflected, extra := tt.layout.Sender(padding[:tt.pad])
			if len(reflected) != tt.reflected || len(extra) != tt.extra {
				t.Errorf("Sender = %d/%d, want %d/%d", len(reflected), len(extra), tt.reflected, tt.extra)
			}
			if len(reflected) > 0 && (reflected[0] != 0 || cap(reflected) != len(reflected)) {
				t.Errorf("reflected octets %v (cap %d): want the start of the padding, closed", reflected, cap(reflected))
			}
			r, e := tt.layout.Reflector(padding[:tt.pad])
			if len(r) != tt.reflected || len(e) != tt.extra {
				t.Errorf("Reflector = %d/%d", len(r), len(e))
			}
			if h, a := tt.layout.SenderHeaderSize(wire.SecurityUnauthenticated), tt.layout.SenderHeaderSize(wire.SecurityEncrypted); h != tt.header || a != tt.authHdr {
				t.Errorf("SenderHeaderSize = %d/%d, want %d/%d", h, a, tt.header, tt.authHdr)
			}
			for _, c := range []struct {
				mode  wire.SecurityMode
				least uint32
			}{{wire.SecurityUnauthenticated, tt.least}, {wire.SecurityAuthenticated, tt.authLeast}} {
				if err := tt.layout.Validate(c.mode, c.least); err != nil {
					t.Errorf("%v: Validate(%d) = %v", c.mode, c.least, err)
				}
				if c.least > 0 && tt.layout.Validate(c.mode, c.least-1) == nil {
					t.Errorf("%v: Validate(%d) accepted", c.mode, c.least-1)
				}
			}
		})
	}
}
