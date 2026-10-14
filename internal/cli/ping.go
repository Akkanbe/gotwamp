package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
)

// pingFlags are the flags of gotwamp ping (SPEC §10.3).
type pingFlags struct {
	light          bool
	controlPort    uint32
	receiverPort   uint32
	senderPort     uint32
	count          uint32
	interval       seconds
	timeout        seconds
	padding        uint32
	paddingFill    string
	dscp           string
	ttl            uint32
	source         string
	ipv4, ipv6     bool
	zeroAddresses  bool
	startTime      string
	controlTimeout seconds
	reflectorSeq   string
	dscpECN        bool
	security       string
	keyID          string
	// The TWAMP extensions of Full (RFC 5938, RFC 6038) and STAMP of Light.
	isc, symmetrical bool
	reflectOctets    uint32
	paddingToReflect uint32
	stamp            bool
	ssid             uint32
	quiet            bool
}

// seconds is a duration flag that also takes plain seconds, as twping does:
// "0.1", "100ms" and "1s" are all accepted.
type seconds time.Duration

func (s *seconds) String() string { return time.Duration(*s).String() }

func (s *seconds) Set(v string) error {
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		*s = seconds(f * float64(time.Second))
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%q is neither seconds nor a duration", v)
	}
	*s = seconds(d)
	return nil
}

func (*seconds) Type() string { return "duration" }

// newTestFlags returns the test parameter flags; call register to add
// them to a command.
//
//declscope:package // session add and update take the same test flags
func newTestFlags() *pingFlags { return &pingFlags{} }

// newPingCommand returns "gotwamp ping".
//
//declscope:package // a subcommand of the root
func newPingCommand(g *globals) *cobra.Command {
	var pf pingFlags
	cmd := &cobra.Command{
		Use:   "ping TARGET",
		Short: "Run an ad-hoc measurement through gotwampd",
		Long: "Run an ad-hoc TWAMP measurement through gotwampd, printing each reply as it\n" +
			"arrives and a summary at the end. Flags left unset take gotwampd's sender.defaults.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := pf.spec(cmd.Flags(), args[0])
			if err != nil {
				return err
			}
			return runPing(cmd.Context(), g, cmd.OutOrStdout(), spec, pf.quiet)
		},
	}
	pf.register(cmd.Flags())
	cmd.Flags().BoolVarP(&pf.quiet, "quiet", "q", false, "print the summary only")
	return cmd
}

// register adds the test parameter flags of SPEC §10.3 to f.
//
//declscope:package // session add takes the same flags
func (pf *pingFlags) register(f *pflag.FlagSet) {
	f.BoolVar(&pf.light, "light", false, "TWAMP Light (no TWAMP-Control)")
	f.Uint32VarP(&pf.controlPort, "control-port", "p", 862, "TWAMP-Control port (Full)")
	f.Uint32VarP(&pf.receiverPort, "port", "r", 0, "reflector UDP port (Full: the Sender Port; Light: 862)")
	f.Uint32Var(&pf.receiverPort, "receiver-port", 0, "same as --port")
	f.Uint32Var(&pf.senderPort, "sender-port", 0, "sender UDP port (0 picks one from 49152-57343)")
	f.Uint32VarP(&pf.count, "count", "c", 100, "number of packets")
	pf.interval = seconds(100 * time.Millisecond)
	f.VarP(&pf.interval, "interval", "i", "interval between packets (0.1, 100ms, 1s)")
	pf.timeout = seconds(2 * time.Second)
	f.VarP(&pf.timeout, "timeout", "L", "loss timeout")
	f.Uint32VarP(&pf.padding, "padding", "s", 27, "padding octets")
	f.StringVar(&pf.paddingFill, "padding-fill", "auto", "padding content: auto, random or zero")
	f.StringVarP(&pf.dscp, "dscp", "D", "0", "DSCP value or name (ef, af41, cs5, ...)")
	f.Uint32Var(&pf.ttl, "ttl", 255, "TTL / Hop Limit")
	f.StringVarP(&pf.source, "source", "S", "", "source address")
	f.BoolVarP(&pf.ipv4, "ipv4", "4", false, "use IPv4")
	f.BoolVarP(&pf.ipv6, "ipv6", "6", false, "use IPv6")
	f.BoolVarP(&pf.zeroAddresses, "zero-addresses", "Z", false, "send zero addresses in Request-TW-Session (NAT)")
	f.StringVar(&pf.startTime, "start-time", "zero", "Start Time field: zero or now")
	pf.controlTimeout = seconds(15 * time.Second)
	f.Var(&pf.controlTimeout, "control-timeout", "TWAMP-Control timeout")
	f.StringVar(&pf.reflectorSeq, "reflector-sequence", "stateful",
		"how the reflector numbers replies: stateful or copy_sender (no loss split)")
	f.BoolVar(&pf.dscpECN, "dscp-ecn", false, "read the DSCP and ECN the reflector received (RFC 7750, Light only)")
	f.StringVar(&pf.security, "security", "unauthenticated",
		"TWAMP-Control mode: unauthenticated, auth, encrypted or mixed (Full only; the key comes from gotwampd's sender.keys_file)")
	f.StringVar(&pf.keyID, "key-id", "", "KeyID of the shared secret in gotwampd's sender.keys_file")
	f.BoolVar(&pf.isc, "isc", false, "use Individual Session Control where the server offers it (RFC 5938, Full)")
	f.BoolVar(&pf.symmetrical, "symmetrical", false, "use Symmetrical Size where the server offers it (RFC 6038, Full)")
	f.Uint32Var(&pf.reflectOctets, "reflect-octets", 0,
		"two octets, 0-65535 (0x... too), the server must echo in Accept-Session: RFC 6038 \"Octets to be reflected\" (Full)")
	f.Uint32Var(&pf.paddingToReflect, "padding-to-reflect", 0,
		"octets of padding the reflector must return unchanged: RFC 6038 \"Length of padding to reflect\" (Full; -s must be longer)")
	f.BoolVar(&pf.stamp, "stamp", false, "send STAMP packets (RFC 8762, Light)")
	f.Uint32Var(&pf.ssid, "ssid", 0, "STAMP Session-Sender Identifier, 1-65535 (RFC 8972, with --stamp)")
}

// spec builds the request. Only the flags given on the command line are
// set, so that the others, the count included, take gotwampd's defaults
// (ADR 0005).
//
//declscope:package // session add builds its test the same way
func (pf *pingFlags) spec(fs *pflag.FlagSet, target string) (*gotwampv1.TestSpec, error) {
	set := fs.Changed
	s := &gotwampv1.TestSpec{Target: target, Mode: gotwampv1.Mode_MODE_FULL, SourceAddress: pf.source}
	if pf.light {
		s.Mode = gotwampv1.Mode_MODE_LIGHT
		// TWAMP Light has no control connection: -p would be ignored and
		// the packets would go to 862.
		if set("control-port") {
			return nil, exitf(ExitUsage, "--control-port (-p) is the TWAMP-Control port of Full; with --light give the reflector port with -r/--port")
		}
	}
	switch {
	case pf.ipv4 && pf.ipv6:
		return nil, exitf(ExitUsage, "-4 and -6 exclude each other")
	case pf.ipv4:
		s.Ipv = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV4
	case pf.ipv6:
		s.Ipv = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6
	}
	// Zero in these fields means "the default" on the wire to gotwampd, so
	// a zero given on the command line is refused rather than replaced.
	if set("control-port") {
		if pf.controlPort < 1 || pf.controlPort > 65535 {
			return nil, exitf(ExitUsage, "--control-port %d is not in 1-65535", pf.controlPort)
		}
		s.ControlPort = pf.controlPort
	}
	if set("port") || set("receiver-port") {
		s.ReceiverPort = &pf.receiverPort
	}
	if set("sender-port") {
		s.SenderPort = &pf.senderPort
	}
	if set("count") {
		s.Count = &pf.count
	}
	if set("interval") {
		s.Interval = durationpb.New(time.Duration(pf.interval))
	}
	if set("timeout") {
		s.Timeout = durationpb.New(time.Duration(pf.timeout))
	}
	if set("padding") {
		s.Padding = &pf.padding
	}
	if set("padding-fill") {
		switch pf.paddingFill {
		case "auto":
			s.PaddingFill = gotwampv1.PaddingFill_PADDING_FILL_AUTO
		case "random":
			s.PaddingFill = gotwampv1.PaddingFill_PADDING_FILL_RANDOM
		case "zero":
			s.PaddingFill = gotwampv1.PaddingFill_PADDING_FILL_ZERO
		default:
			return nil, exitf(ExitUsage, "--padding-fill %q is not auto, random or zero", pf.paddingFill)
		}
	}
	if set("dscp") {
		d, err := config.ParseDSCP(pf.dscp)
		if err != nil {
			return nil, exitf(ExitUsage, "--dscp: %w", err)
		}
		v := uint32(d)
		s.Dscp = &v
	}
	if set("ttl") {
		if pf.ttl < 1 || pf.ttl > 255 {
			return nil, exitf(ExitUsage, "--ttl %d is not in 1-255", pf.ttl)
		}
		s.Ttl = pf.ttl
	}
	if set("dscp-ecn") {
		s.DscpEcnMonitoring = &pf.dscpECN
	}
	if set("security") {
		if pf.light && pf.security != "unauthenticated" && pf.security != "open" {
			return nil, exitf(ExitUsage, "--security applies to TWAMP-Control (Full); TWAMP Light is unauthenticated only")
		}
		switch pf.security {
		case "unauthenticated", "open":
			s.Security = gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED
		case "auth", "authenticated":
			s.Security = gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED
		case "encrypted":
			s.Security = gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED
		case "mixed":
			s.Security = gotwampv1.SecurityMode_SECURITY_MODE_MIXED
		default:
			return nil, exitf(ExitUsage, "--security %q is not unauthenticated, auth, encrypted or mixed", pf.security)
		}
	}
	if set("key-id") {
		if pf.light {
			return nil, exitf(ExitUsage, "--key-id applies to TWAMP-Control (Full), not --light")
		}
		s.KeyId = pf.keyID
	}
	if set("control-timeout") {
		s.ControlTimeout = durationpb.New(time.Duration(pf.controlTimeout))
	}
	if err := pf.extensions(set, s); err != nil {
		return nil, err
	}
	var compat gotwampv1.CompatOptions
	if set("zero-addresses") {
		compat.ZeroAddresses = &pf.zeroAddresses
		s.Compat = &compat
	}
	if set("start-time") {
		switch pf.startTime {
		case "zero":
			compat.StartTime = gotwampv1.StartTime_START_TIME_ZERO
		case "now":
			compat.StartTime = gotwampv1.StartTime_START_TIME_NOW
		default:
			return nil, exitf(ExitUsage, "--start-time %q is not zero or now", pf.startTime)
		}
		s.Compat = &compat
	}
	if set("reflector-sequence") {
		switch pf.reflectorSeq {
		case "stateful":
			compat.ReflectorSequence = gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_STATEFUL
		case "copy_sender":
			compat.ReflectorSequence = gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_COPY_SENDER
		default:
			return nil, exitf(ExitUsage, "--reflector-sequence %q is not stateful or copy_sender", pf.reflectorSeq)
		}
		s.Compat = &compat
	}
	return s, nil
}

// extensions sets the TWAMP extensions of Full and STAMP of Light, each
// for its mode only.
func (pf *pingFlags) extensions(set func(string) bool, s *gotwampv1.TestSpec) error {
	for _, name := range []string{"isc", "symmetrical", "reflect-octets", "padding-to-reflect"} {
		if pf.light && set(name) {
			return exitf(ExitUsage, "--%s applies to TWAMP Full, not --light", name)
		}
	}
	for _, name := range []string{"stamp", "ssid"} {
		if !pf.light && set(name) {
			return exitf(ExitUsage, "--%s applies to TWAMP Light: add --light", name)
		}
	}
	if pf.isc {
		s.Extensions = append(s.Extensions, gotwampv1.Extension_EXTENSION_INDIVIDUAL_SESSION_CONTROL)
	}
	if pf.symmetrical {
		s.Extensions = append(s.Extensions, gotwampv1.Extension_EXTENSION_SYMMETRICAL_SIZE)
	}
	if set("reflect-octets") {
		if pf.reflectOctets > 65535 {
			return exitf(ExitUsage, "--reflect-octets %d is not in 0-65535", pf.reflectOctets)
		}
		s.OctetsToReflect = &pf.reflectOctets
	}
	if set("padding-to-reflect") {
		if pf.paddingToReflect > 65535 {
			return exitf(ExitUsage, "--padding-to-reflect %d is not in 0-65535", pf.paddingToReflect)
		}
		s.PaddingToReflect = &pf.paddingToReflect
	}
	if set("stamp") {
		s.Stamp = &pf.stamp
	}
	if set("ssid") {
		if !pf.stamp || pf.ssid > 65535 {
			return exitf(ExitUsage, "--ssid %d needs --stamp and 0-65535", pf.ssid)
		}
		s.Ssid = &pf.ssid
	}
	return nil
}

// fragmentWarning warns of test packets too large for one packet on a
// 1500-octet path (SPEC §9.2): the resolved padding is known only now.
func fragmentWarning(s *gotwampv1.RunStarted) string {
	const header = 14 // unauthenticated Session-Sender packet
	limit := 1472     // IPv4: 1500 - 20 - 8
	if a, err := netip.ParseAddr(s.GetRemote().GetAddress()); err == nil && a.Is6() && !a.Is4In6() {
		limit = 1452 // IPv6: 1500 - 40 - 8
	}
	if size := header + int(s.GetSpec().GetPadding()); size > limit {
		return fmt.Sprintf("test packets of %d octets exceed %d, the largest UDP payload unfragmented on a 1500-octet path", size, limit)
	}
	return ""
}

// runDeadline is how long a run of the resolved spec may take from its
// start: count × interval + timeout + end_delay, plus slack for the
// summary and the Stop-Sessions exchange.
func runDeadline(spec *gotwampv1.TestSpec, slack time.Duration) time.Duration {
	run := time.Duration(spec.GetCount())*spec.GetInterval().AsDuration() +
		spec.GetTimeout().AsDuration() + spec.GetCompat().GetEndDelay().AsDuration()
	return max(run, 0) + slack
}

// runPing streams one RunTest and prints it in the chosen output format.
func runPing(ctx context.Context, g *globals, w io.Writer, spec *gotwampv1.TestSpec, quiet bool) error {
	conn, err := g.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	// The RPC lasts as long as the measurement (SPEC §10.1). Only gotwampd
	// knows the parameters the request left to sender.defaults, so the
	// deadline covers the setup until RunStarted brings the resolved spec,
	// and then the run itself (see runDeadline).
	rpcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var expired atomic.Bool
	deadline := time.AfterFunc(2*g.timeout, func() {
		expired.Store(true)
		cancel()
	})
	defer deadline.Stop()
	stream, err := gotwampv1.NewSenderServiceClient(conn).RunTest(rpcCtx, &gotwampv1.RunTestRequest{Spec: spec})
	if err != nil {
		return g.rpcError(err)
	}

	// The first interrupt asks gotwampd to stop the run, which then streams
	// the aborted summary; a second one gives up on the RPC (Main cancels
	// ctx).
	var (
		runID   atomic.Pointer[string]
		stopped atomic.Bool
	)
	g.onInterrupt(func() {
		stopped.Store(true)
		id := runID.Load()
		if id == nil {
			cancel()
			return
		}
		go func() {
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stopCancel()
			if _, err := gotwampv1.NewSenderServiceClient(conn).StopTest(stopCtx, &gotwampv1.StopTestRequest{RunId: *id}); err != nil {
				cancel()
			}
		}()
	})
	defer g.onInterrupt(nil)

	out := newPingPrinter(w, g.output, quiet)
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if expired.Load() {
				return exitf(ExitFailure, "gotwampd did not finish the run in time (--rpc-timeout %v beyond the measurement)", g.timeout)
			}
			if ctx.Err() != nil || stopped.Load() {
				// Interrupted before gotwampd could deliver the summary:
				// show what arrived.
				return errors.Join(out.interrupted(), exitf(ExitInterrupted, ""))
			}
			return g.rpcError(err)
		}
		if s := ev.GetStarted(); s != nil {
			deadline.Reset(runDeadline(s.GetSpec(), g.timeout))
			if msg := fragmentWarning(s); msg != "" {
				g.warn(msg)
			}
		}
		if runID.Load() == nil && ev.GetRunId() != "" {
			id := ev.GetRunId()
			runID.Store(&id)
		}
		if err := out.event(ev); err != nil {
			return err
		}
	}
	if err := out.finish(); err != nil {
		return err
	}
	if stopped.Load() {
		return exitf(ExitInterrupted, "")
	}
	return out.exitStatus()
}
