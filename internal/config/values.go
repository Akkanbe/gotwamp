package config

import (
	"fmt"
	"io/fs"
	"net/netip"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DSCP is a Differentiated Services Code Point, 0-63. In YAML it is a number
// (decimal, or hexadecimal with 0x) or a name: ef, va, csN (N 0-7), afXY
// (X 1-4, Y 1-3), be or default.
type DSCP uint8

// UnmarshalYAML accepts a number or a DSCP name.
func (d *DSCP) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDSCP(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = v
	return nil
}

// ParseDSCP parses a DSCP number (0-63, decimal or 0x hexadecimal) or name.
func ParseDSCP(s string) (DSCP, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	// Decimal, or hexadecimal with 0x. A leading zero is not octal: "010"
	// is 10, as an operator reading the file takes it.
	num, base := name, 10
	if h, ok := strings.CutPrefix(name, "0x"); ok {
		num, base = h, 16
	}
	if v, err := strconv.ParseUint(num, base, 8); err == nil {
		if v > 63 {
			return 0, fmt.Errorf("dscp %d is out of range 0-63", v)
		}
		return DSCP(v), nil
	}
	switch name {
	case "be", "default":
		return 0, nil
	case "ef":
		return 46, nil
	case "va":
		return 44, nil
	}
	if len(name) == 3 && name[:2] == "cs" && name[2] >= '0' && name[2] <= '7' {
		return DSCP(name[2]-'0') << 3, nil
	}
	if len(name) == 4 && name[:2] == "af" && name[2] >= '1' && name[2] <= '4' && name[3] >= '1' && name[3] <= '3' {
		return DSCP(name[2]-'0')<<3 | DSCP(name[3]-'0')<<1, nil
	}
	return 0, fmt.Errorf("dscp %q is neither 0-63 nor a known name (ef, va, csN, afXY, be)", s)
}

// Size is a byte count. In YAML it is a number with an optional unit: B, KiB,
// MiB, GiB (powers of 1024) or KB, MB, GB (powers of 1000).
type Size int64

// UnmarshalYAML accepts a number with an optional unit.
func (z *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := parseSize(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*z = v
	return nil
}

// parseSize parses a byte count such as "100MiB".
func parseSize(s string) (Size, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10},
		{"MiB", 1 << 20},
		{"GiB", 1 << 30},
		{"KB", 1e3},
		{"MB", 1e6},
		{"GB", 1e9},
		{"B", 1},
	}
	num, mult := strings.TrimSpace(s), int64(1)
	for _, u := range units {
		if strings.HasSuffix(num, u.suffix) {
			num, mult = strings.TrimSpace(strings.TrimSuffix(num, u.suffix)), u.mult
			break
		}
	}
	v, err := strconv.ParseInt(num, 10, 64)
	if err != nil || v < 0 || v > (1<<62)/mult {
		return 0, fmt.Errorf("size %q is not a non-negative number with an optional unit (B, KiB, MiB, GiB, KB, MB, GB)", s)
	}
	return Size(v * mult), nil
}

// PortRange is an inclusive range of ports.
type PortRange struct {
	First, Last uint16
}

// ParsePortRange parses "first-last" (or a single port) with 1 <= first <=
// last <= 65535.
func ParsePortRange(s string) (PortRange, error) {
	first, last, found := strings.Cut(strings.TrimSpace(s), "-")
	if !found {
		last = first
	}
	f, err1 := strconv.ParseUint(strings.TrimSpace(first), 10, 16)
	l, err2 := strconv.ParseUint(strings.TrimSpace(last), 10, 16)
	if err1 != nil || err2 != nil || f == 0 || f > l {
		return PortRange{}, fmt.Errorf("port range %q is not first-last with 1 <= first <= last <= 65535", s)
	}
	return PortRange{First: uint16(f), Last: uint16(l)}, nil
}

// ParseAddrPort parses a listen address such as "0.0.0.0:862" or "[::]:862".
// IPv4-mapped IPv6 addresses are unmapped.
func ParseAddrPort(s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(strings.TrimSpace(s))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("%q is not an IP address and port", s)
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), nil
}

// ParsePrefix parses an ACL entry: a CIDR prefix, or a bare address standing
// for a single host.
func ParsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a CIDR prefix or an IP address", s)
	}
	return p.Masked(), nil
}

// securityModes are the values of security and reflector.control.modes.
//
//declscope:package // Validate checks both keys against it
var securityModes = []string{"unauthenticated", "authenticated", "encrypted", "mixed"}

// extensions are the values of extensions: the TWAMP extensions of RFC 5938
// and RFC 6038.
//
//declscope:package // Default offers them all and Validate checks against them
var extensions = []string{"individual_session_control", "reflect_octets", "symmetrical_size"}

// OfferedModes returns the security modes the control server offers: Modes,
// or by default unauthenticated alone, and all four with a keys file.
func (c ControlServer) OfferedModes() []string {
	switch {
	case len(c.Modes) > 0:
		return c.Modes
	case c.KeysFile != "":
		return securityModes
	default:
		return securityModes[:1]
	}
}

// FileMode parses socket_mode, an octal permission such as "0660".
func (a API) FileMode() (fs.FileMode, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(a.SocketMode), 8, 32)
	if err != nil || v > 0o777 {
		return 0, fmt.Errorf("socket mode %q is not an octal permission such as 0660", a.SocketMode)
	}
	return fs.FileMode(v), nil
}
