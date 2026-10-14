package daemon

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/schedule"
	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// defaultLightPort is the reflector port of a Light test that names none.
const defaultLightPort = 862

// testPlan is a test with every default resolved: what the runner needs to
// carry out one run. It is comparable with reflect.DeepEqual, so that the
// scheduler can tell a changed session from an unchanged one.
//
//declscope:package // built here, carried out by the runner (runner.go)
type testPlan struct {
	// host, ipv and source select the target as configured; addr, when
	// valid, is the target already resolved (ad-hoc tests).
	host   string
	ipv    string
	source netip.Addr
	addr   netip.Addr
	full   bool
	// test describes the test packets; its Target is set at each run.
	test sender.Config
	// fc is the control side of a Full run; its Server is set at each run.
	fc sender.FullConfig
	// controlPort is the TWAMP-Control port of a Full run.
	controlPort uint16
	// receiverPort is the reflector port of a Light run.
	receiverPort uint16
	// senderPort fixes the Sender Port of a Light run; zero picks one from
	// senderPorts.
	senderPort  uint16
	senderPorts config.PortRange
}

// sessionOptions are the schedule.SessionSpec Options of a monitoring
// session: its plan, and its definition for GetSession and ListSessions.
//
//declscope:package // the runner and the sender service read it back from the scheduler
type sessionOptions struct {
	plan testPlan
	def  config.Session
}

// newPlan resolves a validated test definition against the daemon settings
// and the sender's keys. A Full test's security mode takes its key from
// keys; TWAMP Light is unauthenticated only and ignores the mode.
//
//declscope:package // RunTest plans ad-hoc tests with it
func newPlan(target, ipv, source string, p config.TestParams, d config.Daemon, keys keystore.Store) (testPlan, error) {
	var secret testSecret
	if p.Mode == "full" {
		var err error
		if secret, err = testSecurity(p, keys); err != nil {
			return testPlan{}, err
		}
	}
	senderPorts, err := config.ParsePortRange(p.SenderPortRange)
	if err != nil {
		return testPlan{}, err
	}
	var src netip.Addr
	if source != "" {
		if src, err = netip.ParseAddr(source); err != nil {
			return testPlan{}, fmt.Errorf("source %q is not an IP address", source)
		}
		src = src.Unmap()
	}
	fill := sender.PaddingAuto
	switch p.PaddingFill {
	case "random":
		fill = sender.PaddingRandom
	case "zero":
		fill = sender.PaddingZero
	}
	if p.Count < 0 || p.ReceiverPort < 0 || p.ReceiverPort > 65535 || p.SenderPort < 0 || p.SenderPort > 65535 ||
		p.ControlPort < 0 || p.ControlPort > 65535 {
		return testPlan{}, errors.New("count or port out of range")
	}
	plan := testPlan{
		host:   target,
		ipv:    ipv,
		source: src,
		full:   p.Mode == "full",
		test: sender.Config{
			Count:              uint32(p.Count), //nolint:gosec // G115: validated to 1-1,000,000
			Interval:           p.Interval,
			Timeout:            p.Timeout,
			Padding:            p.Padding,
			PaddingFill:        fill,
			DSCP:               int(p.DSCP),
			TTL:                p.TTL,
			Timestamping:       timestampingMode(d.Timestamping),
			UserspaceError:     d.Clock.UserspaceError,
			KernelError:        d.Clock.KernelError,
			HardwareError:      d.Clock.HardwareError,
			StatelessReflector: p.ReflectorSequence == "copy_sender",
			DSCPECNMonitoring:  p.DSCPECNMonitoring,
			STAMP:              p.STAMP,
			SSID:               uint16(min(max(p.SSID, 0), 65535)), //nolint:gosec // G115: clamped
			DropZeroMultiplier: !p.TolerateZeroMultiplier,
			MaxSamples:         d.Limits.MaxSamplesPerRun,
			LockOSThread:       d.Realtime.LockOSThread,
			TxTimestampWait:    txTimestampWait(p.TxTimestampWait),
		},
		fc: sender.FullConfig{
			ControlTimeout:      p.ControlTimeout,
			ControlDSCP:         int(p.ControlDSCP),
			SenderPort:          uint16(p.SenderPort), //nolint:gosec // G115: checked above
			SenderPorts:         sender.PortRange{Low: senderPorts.First, High: senderPorts.Last},
			ReceiverPort:        uint16(p.ReceiverPort), //nolint:gosec // G115: checked above
			ZeroAddresses:       p.ZeroAddresses,
			StartTimeNow:        p.StartTime == "now",
			RejectAlternatePort: !p.AcceptAlternatePort,
			EndDelay:            p.EndDelay,
			Modes:               secret.modes,
			KeyID:               secret.keyID,
			Passphrase:          secret.passphrase,
			Features:            featureModes(p.Extensions, p.OctetsToReflect != 0 || p.PaddingToReflect != 0),
			OctetsToReflect:     uint16(min(max(p.OctetsToReflect, 0), 65535)),  //nolint:gosec // G115: clamped
			PaddingToReflect:    uint16(min(max(p.PaddingToReflect, 0), 65535)), //nolint:gosec // G115: clamped
		},
		controlPort:  uint16(p.ControlPort),  //nolint:gosec // G115: checked above
		receiverPort: uint16(p.ReceiverPort), //nolint:gosec // G115: checked above
		senderPort:   uint16(p.SenderPort),   //nolint:gosec // G115: checked above
		senderPorts:  senderPorts,
	}
	if plan.receiverPort == 0 {
		plan.receiverPort = defaultLightPort
	}
	return plan, nil
}

// sessionSpec turns a validated monitoring session into the scheduler's
// definition.
//
//declscope:package // the sender service and reload build sessions with it
func sessionSpec(s config.Session, d config.Daemon, keys keystore.Store) (schedule.SessionSpec, error) {
	plan, err := newPlan(s.Target, s.IPV, s.Source, s.TestParams, d, keys)
	if err != nil {
		return schedule.SessionSpec{}, fmt.Errorf("session %s: %w", s.Name, err)
	}
	spec := schedule.SessionSpec{
		Name:     s.Name,
		Schedule: schedule.Schedule{Type: schedule.Periodic, Frequency: s.Schedule.Frequency, Jitter: s.Schedule.Jitter},
		Info: store.SessionInfo{
			Name:     s.Name,
			Target:   s.Target,
			Mode:     s.Mode,
			Labels:   maps.Clone(s.Labels),
			Count:    plan.test.Count,
			Interval: s.Interval,
			Padding:  s.Padding,
			DSCP:     int(s.DSCP),
			// The results take their TestSpec from it (eventSpec).
			Definition: s,
		},
		Test:    plan.test,
		Options: sessionOptions{plan: plan, def: s},
	}
	if s.Schedule.Type == "continuous" {
		spec.Schedule = schedule.Schedule{Type: schedule.Continuous, Window: s.Schedule.Window}
		spec.Info.Count = 0
	}
	if plan.full {
		spec.Info.ControlPort = plan.controlPort
	}
	return spec, nil
}

// configSessions returns the scheduler definitions of the sessions of cfg.
//
//declscope:package // Start and reload hand them to the scheduler
func configSessions(cfg *config.Config, keys keystore.Store) ([]schedule.SessionSpec, error) {
	specs := make([]schedule.SessionSpec, 0, len(cfg.Sender.Sessions))
	for _, s := range cfg.Sender.Sessions {
		spec, err := sessionSpec(s, cfg.Daemon, keys)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// overlayTest applies the fields a request sets onto p, which holds the
// defaults. Unset fields keep them (ADR 0005).
//
//declscope:package // RunTest and the session RPCs read requests with it
func overlayTest(p *config.TestParams, s *gotwampv1.TestSpec) error {
	switch s.GetMode() {
	case gotwampv1.Mode_MODE_FULL:
		p.Mode = "full"
	case gotwampv1.Mode_MODE_LIGHT:
		p.Mode = "light"
	}
	switch s.GetSecurity() {
	case gotwampv1.SecurityMode_SECURITY_MODE_UNSPECIFIED:
	case gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED:
		p.Security = "unauthenticated"
	case gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED:
		p.Security = "authenticated"
	case gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED:
		p.Security = "encrypted"
	case gotwampv1.SecurityMode_SECURITY_MODE_MIXED:
		p.Security = "mixed"
	}
	if s.GetKeyId() != "" {
		p.KeyID = s.GetKeyId()
	}
	if v := s.GetControlPort(); v != 0 {
		p.ControlPort = int(v)
	}
	if s.ReceiverPort != nil {
		p.ReceiverPort = int(s.GetReceiverPort())
	}
	if s.SenderPort != nil {
		p.SenderPort = int(s.GetSenderPort())
	}
	if s.Count != nil {
		p.Count = int(s.GetCount())
	}
	if s.Interval != nil {
		p.Interval = s.GetInterval().AsDuration()
	}
	if s.Timeout != nil {
		p.Timeout = s.GetTimeout().AsDuration()
	}
	if s.Padding != nil {
		p.Padding = int(s.GetPadding())
	}
	switch s.GetPaddingFill() {
	case gotwampv1.PaddingFill_PADDING_FILL_UNSPECIFIED:
	case gotwampv1.PaddingFill_PADDING_FILL_AUTO:
		p.PaddingFill = "auto"
	case gotwampv1.PaddingFill_PADDING_FILL_RANDOM:
		p.PaddingFill = "random"
	case gotwampv1.PaddingFill_PADDING_FILL_ZERO:
		p.PaddingFill = "zero"
	default:
		return fmt.Errorf("unknown padding fill %v", s.GetPaddingFill())
	}
	if s.Dscp != nil {
		d, err := dscp(s.GetDscp())
		if err != nil {
			return err
		}
		p.DSCP = d
	}
	if v := s.GetTtl(); v != 0 {
		p.TTL = int(v)
	}
	if s.ControlTimeout != nil {
		p.ControlTimeout = s.GetControlTimeout().AsDuration()
	}
	if s.DscpEcnMonitoring != nil {
		p.DSCPECNMonitoring = s.GetDscpEcnMonitoring()
	}
	if len(s.GetExtensions()) > 0 {
		p.Extensions = nil
		for _, e := range s.GetExtensions() {
			name, ok := extensionName(e)
			if !ok {
				return fmt.Errorf("unknown extension %v", e)
			}
			p.Extensions = append(p.Extensions, name)
		}
	}
	if s.OctetsToReflect != nil {
		p.OctetsToReflect = int(s.GetOctetsToReflect())
	}
	if s.PaddingToReflect != nil {
		p.PaddingToReflect = int(s.GetPaddingToReflect())
	}
	if s.Stamp != nil {
		p.STAMP = s.GetStamp()
	}
	if s.Ssid != nil {
		p.SSID = int(s.GetSsid())
	}
	return overlayCompat(p, s.GetCompat())
}

// extensionNames are the configuration names of the TWAMP extensions.
var extensionNames = map[gotwampv1.Extension]string{
	gotwampv1.Extension_EXTENSION_INDIVIDUAL_SESSION_CONTROL: "individual_session_control",
	gotwampv1.Extension_EXTENSION_REFLECT_OCTETS:             "reflect_octets",
	gotwampv1.Extension_EXTENSION_SYMMETRICAL_SIZE:           "symmetrical_size",
}

// extensionName converts an extension of the API.
//
//declscope:package // requests check the extensions they name with it
func extensionName(e gotwampv1.Extension) (string, bool) {
	name, ok := extensionNames[e]
	return name, ok
}

// extensionModes are the Modes bits of the TWAMP extensions.
var extensionModes = map[string]wire.Modes{
	"individual_session_control": wire.ModeIndividualSessionControl,
	"reflect_octets":             wire.ModeReflectOctets,
	"symmetrical_size":           wire.ModeSymmetricalSize,
}

// featureModes converts the extensions of a Full test; reflect, set when
// the test fills a Reflect Octets field, asks for Reflect Octets too.
//
//declscope:package // the control server offers extensions the same way
func featureModes(names []string, reflect bool) wire.Modes {
	var m wire.Modes
	for _, n := range names {
		m |= extensionModes[n]
	}
	if reflect {
		m |= wire.ModeReflectOctets
	}
	return m
}

func overlayCompat(p *config.TestParams, c *gotwampv1.CompatOptions) error {
	if c == nil {
		return nil
	}
	if c.ZeroAddresses != nil {
		p.ZeroAddresses = c.GetZeroAddresses()
	}
	switch c.GetStartTime() {
	case gotwampv1.StartTime_START_TIME_ZERO:
		p.StartTime = "zero"
	case gotwampv1.StartTime_START_TIME_NOW:
		p.StartTime = "now"
	}
	if c.AcceptAlternatePort != nil {
		p.AcceptAlternatePort = c.GetAcceptAlternatePort()
	}
	if c.TolerateZeroMultiplier != nil {
		p.TolerateZeroMultiplier = c.GetTolerateZeroMultiplier()
	}
	if c.ControlDscp != nil {
		d, err := dscp(c.GetControlDscp())
		if err != nil {
			return fmt.Errorf("control %w", err)
		}
		p.ControlDSCP = d
	}
	if c.EndDelay != nil {
		p.EndDelay = c.GetEndDelay().AsDuration()
	}
	if r := c.GetSenderPortRange(); r != nil {
		p.SenderPortRange = fmt.Sprintf("%d-%d", r.GetFirst(), r.GetLast())
	}
	switch c.GetReflectorSequence() {
	case gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_UNSPECIFIED:
	case gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_STATEFUL:
		p.ReflectorSequence = "stateful"
	case gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_COPY_SENDER:
		p.ReflectorSequence = "copy_sender"
	default:
		return fmt.Errorf("unknown reflector sequence %v", c.GetReflectorSequence())
	}
	return nil
}

// txTimestampWait converts tx_timestamp_wait, whose 0 means "do not wait",
// to the sender's convention, where 0 means the default and negative does
// not wait.
func txTimestampWait(d time.Duration) time.Duration {
	if d == 0 {
		return -1
	}
	return d
}

func dscp(v uint32) (config.DSCP, error) {
	if v > 63 {
		return 0, fmt.Errorf("dscp %d is out of range 0-63", v)
	}
	return config.DSCP(v), nil
}

// ipvName converts an address family to its configuration name.
//
//declscope:package // RunTest reads requests with it
func ipvName(f gotwampv1.AddressFamily) string {
	switch f {
	case gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV4:
		return "4"
	case gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6:
		return "6"
	case gotwampv1.AddressFamily_ADDRESS_FAMILY_AUTO:
		return "auto"
	default:
		return ""
	}
}

// testSpecProto reports a resolved test definition.
//
//declscope:package // RunTest reports the spec it runs
func testSpecProto(target, ipv, source string, p config.TestParams) *gotwampv1.TestSpec {
	fam := gotwampv1.AddressFamily_ADDRESS_FAMILY_AUTO
	switch ipv {
	case "4":
		fam = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV4
	case "6":
		fam = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6
	}
	mode := gotwampv1.Mode_MODE_FULL
	if p.Mode == "light" {
		mode = gotwampv1.Mode_MODE_LIGHT
	}
	fill := gotwampv1.PaddingFill_PADDING_FILL_AUTO
	switch p.PaddingFill {
	case "random":
		fill = gotwampv1.PaddingFill_PADDING_FILL_RANDOM
	case "zero":
		fill = gotwampv1.PaddingFill_PADDING_FILL_ZERO
	}
	start := gotwampv1.StartTime_START_TIME_ZERO
	if p.StartTime == "now" {
		start = gotwampv1.StartTime_START_TIME_NOW
	}
	seq := gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_STATEFUL
	if p.ReflectorSequence == "copy_sender" {
		seq = gotwampv1.ReflectorSequence_REFLECTOR_SEQUENCE_COPY_SENDER
	}
	var exts []gotwampv1.Extension
	for e, name := range extensionNames {
		if slices.Contains(p.Extensions, name) {
			exts = append(exts, e)
		}
	}
	slices.Sort(exts)
	var portRange *gotwampv1.PortRange
	if r, err := config.ParsePortRange(p.SenderPortRange); err == nil {
		portRange = &gotwampv1.PortRange{First: uint32(r.First), Last: uint32(r.Last)}
	}
	return &gotwampv1.TestSpec{
		Target:            target,
		Mode:              mode,
		Ipv:               fam,
		SourceAddress:     source,
		ControlPort:       u32(p.ControlPort),
		ReceiverPort:      ptr(u32(p.ReceiverPort)),
		SenderPort:        ptr(u32(p.SenderPort)),
		Count:             ptr(u32(p.Count)),
		Interval:          durationpb.New(p.Interval),
		Timeout:           durationpb.New(p.Timeout),
		Padding:           ptr(u32(p.Padding)),
		PaddingFill:       fill,
		Dscp:              ptr(uint32(p.DSCP)),
		Ttl:               u32(p.TTL),
		Security:          protoSecurity(p.Security),
		KeyId:             p.KeyID,
		ControlTimeout:    durationpb.New(p.ControlTimeout),
		DscpEcnMonitoring: ptr(p.DSCPECNMonitoring),
		Extensions:        exts,
		OctetsToReflect:   ptr(u32(p.OctetsToReflect)),
		PaddingToReflect:  ptr(u32(p.PaddingToReflect)),
		Stamp:             ptr(p.STAMP),
		Ssid:              ptr(u32(p.SSID)),
		Compat: &gotwampv1.CompatOptions{
			ZeroAddresses:          ptr(p.ZeroAddresses),
			StartTime:              start,
			AcceptAlternatePort:    ptr(p.AcceptAlternatePort),
			TolerateZeroMultiplier: ptr(p.TolerateZeroMultiplier),
			ControlDscp:            ptr(uint32(p.ControlDSCP)),
			EndDelay:               durationpb.New(p.EndDelay),
			SenderPortRange:        portRange,
			ReflectorSequence:      seq,
		},
	}
}

// protoSecurity converts a security mode of the configuration.
func protoSecurity(s string) gotwampv1.SecurityMode {
	switch s {
	case "authenticated":
		return gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED
	case "encrypted":
		return gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED
	case "mixed":
		return gotwampv1.SecurityMode_SECURITY_MODE_MIXED
	default:
		return gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED
	}
}

// sessionFromProto reads a monitoring session of the API on top of the
// sender defaults.
//
//declscope:package // the session RPCs read requests with it
func sessionFromProto(defaults config.TestParams, ps *gotwampv1.SessionSpec) (config.Session, error) {
	s := config.Session{
		Name:       ps.GetName(),
		Target:     ps.GetSpec().GetTarget(),
		IPV:        ipvName(ps.GetSpec().GetIpv()),
		Source:     ps.GetSpec().GetSourceAddress(),
		Labels:     maps.Clone(ps.GetLabels()),
		TestParams: defaults,
	}
	if err := overlayTest(&s.TestParams, ps.GetSpec()); err != nil {
		return config.Session{}, err
	}
	s.DropInherited(defaults)
	sc := ps.GetSchedule()
	switch sc.GetType() {
	case gotwampv1.ScheduleType_SCHEDULE_TYPE_UNSPECIFIED, gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC:
		s.Schedule.Type = "periodic"
	case gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS:
		s.Schedule.Type = "continuous"
	default:
		return config.Session{}, fmt.Errorf("unknown schedule type %v", sc.GetType())
	}
	s.Schedule.Frequency = sc.GetFrequency().AsDuration()
	s.Schedule.Jitter = sc.GetJitter().AsDuration()
	s.Schedule.Window = sc.GetWindow().AsDuration()
	return s, nil
}

// sessionSpecProto reports a monitoring session definition.
//
//declscope:package // the session RPCs report definitions with it
func sessionSpecProto(s config.Session) *gotwampv1.SessionSpec {
	sc := &gotwampv1.Schedule{Type: gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC}
	if s.Schedule.Type == "continuous" {
		sc.Type = gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS
		sc.Window = durationpb.New(s.Schedule.Window)
	} else {
		sc.Frequency = durationpb.New(s.Schedule.Frequency)
		sc.Jitter = durationpb.New(s.Schedule.Jitter)
	}
	return &gotwampv1.SessionSpec{
		Name:     s.Name,
		Spec:     testSpecProto(s.Target, s.IPV, s.Source, s.TestParams),
		Schedule: sc,
		Labels:   maps.Clone(s.Labels),
	}
}

func u32(v int) uint32 { return uint32(min(max(v, 0), 1<<32-1)) } //nolint:gosec // G115: clamped

func ptr[T any](v T) *T { return &v }
