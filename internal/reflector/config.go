package reflector

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// Defaults of the Light reflector (SPEC §9.2, §9.9, appendix A).
//
//declscope:ignore overexported // the exported ones document the defaults the tests check against
const (
	DefaultFlowTimeout    = 900 * time.Second // REFWAIT
	defaultMaxFlows       = 65536
	DefaultUserspaceError = 50 * time.Microsecond
)

// Defaults the Light and the Full reflector share (SPEC appendix A).
//
//declscope:package // Config and FullConfig take the same defaults
const (
	defaultTTL           = 255
	defaultMaxPacketSize = 9000
	defaultKernelError   = 5 * time.Microsecond
)

// maxUDPPayload is the largest UDP payload over IPv4.
//
//declscope:package // bounds MaxPacketSize in Config and FullConfig
const maxUDPPayload = 65507

// DefaultListeners are the Light listeners used when none are configured.
//
//declscope:ignore overexported // documents the default; gotwampd's configuration lists its listeners itself
var DefaultListeners = []netip.AddrPort{
	netip.MustParseAddrPort("[::]:862"),
	netip.MustParseAddrPort("0.0.0.0:862"),
}

// Sequence selects how the Light reflector fills the Reflector Sequence
// Number (SPEC §6.6).
type Sequence uint8

// Sequence values.
//
//declscope:ignore overexported // SequencePerFlow is the zero value, named for a configuration that says so; gotwampd leaves it unset
const (
	// SequencePerFlow counts from 0 per flow, the default.
	SequencePerFlow Sequence = iota
	// SequenceCopySender copies the Sender Sequence Number (stateless,
	// RFC 5357 Appendix I).
	SequenceCopySender
)

// DSCPPolicy selects the DSCP of reflected Light packets (SPEC §6.4 step 7).
type DSCPPolicy uint8

// DSCPPolicy values.
//
//declscope:ignore overexported // DSCPCopy is the zero value, named for a configuration that says so; gotwampd leaves it unset
const (
	// DSCPCopy copies the DSCP of each received packet, the default.
	DSCPCopy DSCPPolicy = iota
	// DSCPFixed sends every reply with Config.DSCPFixed.
	DSCPFixed
)

// Config configures the Light reflector. Zero values take the defaults
// above; Sequence and DSCPPolicy default to their zero values.
type Config struct {
	// Listeners are the UDP addresses to listen on; empty means DefaultListeners.
	Listeners []netip.AddrPort
	// ACL lists the source prefixes allowed; empty allows every source.
	ACL []netip.Prefix
	// Sequence selects per-flow counters or copying the sender's number.
	Sequence Sequence
	// FlowTimeout drops the state of a flow idle for this long.
	FlowTimeout time.Duration
	// MaxFlows caps the flow table; new flows beyond it are handled statelessly.
	MaxFlows int
	// RateLimitPPS is the per-source packet rate; 0 means unlimited.
	RateLimitPPS int
	// DSCPPolicy selects copying or fixing the DSCP of replies.
	DSCPPolicy DSCPPolicy
	// DSCPFixed is the DSCP (0–63) used with DSCPFixed.
	DSCPFixed uint8
	// DSCPECNMonitoring answers in the RFC 7750 layout, reporting the DSCP
	// and ECN of each received packet at offset 41 (TWAMP Light takes it from
	// configuration, RFC 7750 §2.2.3).
	DSCPECNMonitoring bool
	// STAMP answers in the STAMP format (RFC 8762 §4.3.1, SPEC §6.7): the
	// SSID at offset 14 copied (RFC 8972 §3), a 44-octet base with offsets
	// 41–43 MBZ, and the octets after the base copied back. When they start
	// with the flags a sender sets on a TLV (100xxxxx), they are the TLVs of
	// RFC 8972, marked as not understood; otherwise they are TWAMP padding,
	// left alone. A TWAMP sender is answered too. It excludes
	// DSCPECNMonitoring, whose octet STAMP keeps MBZ.
	STAMP bool
	// TTL is the TTL or Hop Limit of replies (1–255).
	TTL int
	// MaxPacketSize drops longer UDP payloads.
	MaxPacketSize int
	// Timestamping selects how receive timestamps are taken.
	Timestamping tstamp.Mode
	// ReadBuffer is SO_RCVBUF; 0 leaves the tstamp default.
	ReadBuffer int
	// UserspaceError and KernelError are the errors of the two timestamping
	// methods. A reply carries one estimate for T2 and T3, and T3 is always
	// taken in user space, so the larger of the two is added to the clock
	// error in the Error Estimate, whatever the timestamping (SPEC §8.4).
	UserspaceError time.Duration
	KernelError    time.Duration
}

// WithDefaults returns c with zero values replaced by the defaults.
func (c Config) WithDefaults() Config {
	if len(c.Listeners) == 0 {
		c.Listeners = slices.Clone(DefaultListeners)
	}
	if c.FlowTimeout == 0 {
		c.FlowTimeout = DefaultFlowTimeout
	}
	if c.MaxFlows == 0 {
		c.MaxFlows = defaultMaxFlows
	}
	if c.TTL == 0 {
		c.TTL = defaultTTL
	}
	if c.MaxPacketSize == 0 {
		c.MaxPacketSize = defaultMaxPacketSize
	}
	if c.UserspaceError == 0 {
		c.UserspaceError = DefaultUserspaceError
	}
	if c.KernelError == 0 {
		c.KernelError = defaultKernelError
	}
	return c
}

// Validate checks a configuration that already has its defaults.
func (c Config) Validate() error {
	var errs []error
	seen := make(map[netip.AddrPort]bool, len(c.Listeners))
	for _, l := range c.Listeners {
		if !l.IsValid() {
			errs = append(errs, fmt.Errorf("listener %v is not an address and port", l))
		}
		if seen[l] {
			errs = append(errs, fmt.Errorf("listener %v is listed twice", l))
		}
		seen[l] = true
	}
	for _, p := range c.ACL {
		if !p.IsValid() {
			errs = append(errs, fmt.Errorf("acl entry %v is not a prefix", p))
		}
	}
	if c.Sequence > SequenceCopySender {
		errs = append(errs, fmt.Errorf("unknown sequence mode %d", c.Sequence))
	}
	if c.FlowTimeout < 0 {
		errs = append(errs, fmt.Errorf("flow timeout %v is negative", c.FlowTimeout))
	}
	if c.MaxFlows < 0 {
		errs = append(errs, fmt.Errorf("max flows %d is negative", c.MaxFlows))
	}
	if c.RateLimitPPS < 0 {
		errs = append(errs, fmt.Errorf("rate limit %d pps is negative", c.RateLimitPPS))
	}
	if c.STAMP && c.DSCPECNMonitoring {
		errs = append(errs, errors.New("stamp and dscp_ecn_monitoring exclude each other: STAMP keeps offset 41 MBZ (RFC 8762 §4.3.1)"))
	}
	if c.DSCPPolicy > DSCPFixed {
		errs = append(errs, fmt.Errorf("unknown dscp policy %d", c.DSCPPolicy))
	}
	if c.DSCPFixed > 63 {
		errs = append(errs, fmt.Errorf("dscp %d is out of range 0-63", c.DSCPFixed))
	}
	if c.TTL < 1 || c.TTL > 255 {
		errs = append(errs, fmt.Errorf("ttl %d is out of range 1-255", c.TTL))
	}
	if c.MaxPacketSize < 14 || c.MaxPacketSize > maxUDPPayload {
		errs = append(errs, fmt.Errorf("max packet size %d is out of range 14-%d", c.MaxPacketSize, maxUDPPayload))
	}
	if c.UserspaceError < 0 || c.KernelError < 0 {
		errs = append(errs, errors.New("timestamping error is negative"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("light reflector config: %w", err)
	}
	return nil
}
