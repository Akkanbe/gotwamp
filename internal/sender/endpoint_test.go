//declscope:namespace sender // endpoint canonicalization belongs to sender configuration

package sender

import (
	"net/netip"
	"testing"
)

func TestNormalizeEndpointRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, wantSource, wantTarget string
		invalid                                      bool
	}{
		{"mapped source", "[::ffff:127.0.0.1]:12345", "127.0.0.1:862", "127.0.0.1:12345", "127.0.0.1:862", false},
		{"mapped target", "127.0.0.1:12345", "[::ffff:127.0.0.1]:862", "127.0.0.1:12345", "127.0.0.1:862", false},
		{"both mapped", "[::ffff:127.0.0.1]:12345", "[::ffff:127.0.0.1]:862", "127.0.0.1:12345", "127.0.0.1:862", false},
		{"ipv6", "[::1]:12345", "[::1]:862", "[::1]:12345", "[::1]:862", false},
		{"mapped versus ipv6", "[::ffff:127.0.0.1]:0", "[::1]:862", "", "", true},
		{"ipv6 versus ipv4", "[::1]:0", "127.0.0.1:862", "", "", true},
	} {
		for _, full := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/light", true: "/full"}[full], func(t *testing.T) {
				cfg := DefaultConfig(netip.MustParseAddrPort(tc.target))
				cfg.Source = netip.MustParseAddrPort(tc.source)
				before := cfg
				var err error
				if full {
					err = ValidateFull(cfg, FullConfig{Server: cfg.Target})
				} else {
					err = cfg.Validate()
				}
				if (err != nil) != tc.invalid {
					t.Errorf("validation error=%v, invalid=%v", err, tc.invalid)
				}
				if cfg != before {
					t.Error("validation mutated caller configuration")
				}
				err = normalize(&cfg, full)
				if (err != nil) != tc.invalid {
					t.Fatalf("normalize error=%v, invalid=%v", err, tc.invalid)
				}
				if !tc.invalid && (cfg.Source.String() != tc.wantSource || cfg.Target.String() != tc.wantTarget) {
					t.Errorf("source=%v target=%v", cfg.Source, cfg.Target)
				}
			})
		}
	}
}
