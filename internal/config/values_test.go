package config

import (
	"io/fs"
	"net/netip"
	"testing"
)

func TestParseDSCP(t *testing.T) {
	good := map[string]DSCP{
		"0": 0, "46": 46, "63": 63, "0x2e": 46, "010": 10, "046": 46, "0X2E": 46, "ef": 46, "EF": 46, "va": 44, "be": 0, "default": 0,
		"cs0": 0, "cs1": 8, "cs5": 40, "cs7": 56, "af11": 10, "af12": 12, "af13": 14, "af41": 34, "af43": 38,
	}
	for s, want := range good {
		if got, err := ParseDSCP(s); err != nil || got != want {
			t.Errorf("ParseDSCP(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"64", "-1", "cs8", "af51", "af14", "ef1", "", "0x40", "0x", "0o7", "0b1"} {
		if _, err := ParseDSCP(s); err == nil {
			t.Errorf("ParseDSCP(%q) succeeded", s)
		}
	}
}

func TestParseSize(t *testing.T) {
	good := map[string]Size{"0": 0, "512": 512, "10B": 10, "1KiB": 1024, "100MiB": 100 << 20, "2GiB": 2 << 30, "3KB": 3000, "5 MB": 5e6, "1GB": 1e9}
	for s, want := range good {
		if got, err := parseSize(s); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "MiB", "-1", "1.5MiB", "1TiB", "99999999999999GiB"} {
		if _, err := parseSize(s); err == nil {
			t.Errorf("parseSize(%q) succeeded", s)
		}
	}
}

func TestParsePortRange(t *testing.T) {
	if r, err := ParsePortRange("49152-57343"); err != nil || r != (PortRange{49152, 57343}) {
		t.Errorf("got %v, %v", r, err)
	}
	if r, err := ParsePortRange("862"); err != nil || r != (PortRange{862, 862}) {
		t.Errorf("got %v, %v", r, err)
	}
	for _, s := range []string{"", "0-10", "10-9", "1-65536", "a-b"} {
		if _, err := ParsePortRange(s); err == nil {
			t.Errorf("ParsePortRange(%q) succeeded", s)
		}
	}
}

func TestParseAddrPortAndPrefix(t *testing.T) {
	if ap, err := ParseAddrPort("[::ffff:192.0.2.1]:862"); err != nil || ap != netip.MustParseAddrPort("192.0.2.1:862") {
		t.Errorf("ParseAddrPort mapped = %v, %v", ap, err)
	}
	if _, err := ParseAddrPort("localhost:862"); err == nil {
		t.Error("ParseAddrPort accepted a host name")
	}
	if p, err := ParsePrefix("192.0.2.7"); err != nil || p != netip.MustParsePrefix("192.0.2.7/32") {
		t.Errorf("ParsePrefix(host) = %v, %v", p, err)
	}
	if p, err := ParsePrefix("10.1.2.3/8"); err != nil || p != netip.MustParsePrefix("10.0.0.0/8") {
		t.Errorf("ParsePrefix(unmasked) = %v, %v", p, err)
	}
}

func TestFileMode(t *testing.T) {
	if m, err := (API{SocketMode: "0660"}).FileMode(); err != nil || m != fs.FileMode(0o660) {
		t.Errorf("FileMode = %v, %v", m, err)
	}
	for _, s := range []string{"", "rw", "1000", "0x1"} {
		if _, err := (API{SocketMode: s}).FileMode(); err == nil {
			t.Errorf("FileMode(%q) succeeded", s)
		}
	}
}
