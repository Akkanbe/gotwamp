package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/export/prom"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// reflectors is the reflector role: the Light reflector and the TWAMP-Control
// server with its Full Session-Reflector. It serves the ReflectorService. A
// nil *reflectors is a disabled role.
//
//declscope:package // Start builds it; the daemon service and the exporter read it
type reflectors struct {
	gotwampv1.UnimplementedReflectorServiceServer
	// disabled marks the service of a daemon without the reflector role.
	//
	//declscope:private
	disabled bool
	//declscope:private
	light *reflector.Light
	//declscope:private
	full *reflector.FullService
}

// startReflectors binds every listener of the reflector role. Call serve to
// start reflecting.
//
//declscope:package // Start builds the reflector role with it
func startReflectors(cfg *config.Config, keys keystore.Store, open *socketOpener, clockState reflector.ClockState, log *slog.Logger) (*reflectors, error) {
	r := &reflectors{}
	lcfg, err := lightConfig(cfg)
	if err != nil {
		return nil, err
	}
	if len(lcfg.Listeners) > 0 {
		if r.light, err = reflector.ListenLight(lcfg, reflector.Deps{Listen: open.listen, Clock: clockState, Logger: log.With("component", "light")}); err != nil {
			return nil, err
		}
		log.Info("light reflector listening", "addresses", r.light.Addrs())
	}

	c := cfg.Reflector.Control
	if len(c.Listen) == 0 {
		return r, nil
	}
	verify := reflector.VerifyAddress
	switch c.VerifySender {
	case "address_port":
		verify = reflector.VerifyAddressPort
	case "none":
		verify = reflector.VerifyNone
	}
	fcfg, err := controlConfig(c, keys)
	if err != nil {
		r.close()
		return nil, err
	}
	fcfg.VerifySender = verify
	fcfg.TTL = cfg.Reflector.TTL
	fcfg.MaxPacketSize = cfg.Reflector.MaxPacketSize
	fcfg.Timestamping = timestampingMode(cfg.Daemon.Timestamping)
	fcfg.UserspaceError = cfg.Daemon.Clock.UserspaceError
	fcfg.KernelError = cfg.Daemon.Clock.KernelError
	for _, s := range c.Listen {
		ap, err := config.ParseAddrPort(s)
		if err != nil {
			r.close()
			return nil, fmt.Errorf("reflector.control.listen: %w", err)
		}
		fcfg.Listeners = append(fcfg.Listeners, ap)
	}
	r.full, err = reflector.ListenFull(fcfg, reflector.Deps{
		Listen: open.listen, Clock: clockState, Logger: log.With("component", "full"),
		ControlLogger: log.With("component", "control"),
		ListenControl: listenControl,
	})
	if err != nil {
		r.close()
		return nil, err
	}
	log.Info("TWAMP-Control server listening", "addresses", r.controlAddrs(), "modes", c.OfferedModes(), "extensions", c.Extensions)
	return r, nil
}

// listenControl retains the daemon's keepalive check over the shared listener.
func listenControl(network string, ap netip.AddrPort) (net.Listener, error) {
	return reflector.ListenControl(network, ap)
}

// controlConfig translates reflector.control; keys are its keys file, nil
// without one.
func controlConfig(c config.ControlServer, keys keystore.Store) (reflector.FullConfig, error) {
	ports, err := config.ParsePortRange(c.TestPortRange)
	if err != nil {
		return reflector.FullConfig{}, err
	}
	out := reflector.FullConfig{
		MaxConnections:           c.MaxConnections,
		MaxSessionsPerConnection: c.MaxSessionsPerConnection,
		MaxSessions:              c.MaxSessions,
		ServWait:                 c.ServWait,
		RefWait:                  c.RefWait,
		PartialMessageTimeout:    c.PartialMessageTimeout,
		TestPorts:                reflector.PortRange{Low: ports.First, High: ports.Last},
		IgnoreRequestedPort:      !c.HonorRequestedPort,
		MaxPadding:               uint32(max(c.MaxPadding, 0)),  //nolint:gosec // G115: validated to 0-65507
		Count:                    uint32(max(c.PBKDF2Count, 0)), //nolint:gosec // G115: validated to 1024-32768
		Modes:                    offeredModes(c) | featureModes(c.Extensions, false),
	}
	if keys != nil {
		// A nil Store in the interface would read as a configured one.
		out.Credentials = keys
	}
	for _, s := range c.ACL {
		p, err := config.ParsePrefix(s)
		if err != nil {
			return reflector.FullConfig{}, fmt.Errorf("reflector.control.acl: %w", err)
		}
		out.ACL = append(out.ACL, p)
	}
	return out, nil
}

// lightConfig translates the reflector section of a validated configuration
// into the Light reflector's configuration.
func lightConfig(cfg *config.Config) (reflector.Config, error) {
	r := cfg.Reflector
	out := reflector.Config{
		FlowTimeout:    r.Light.FlowTimeout,
		MaxFlows:       r.Light.MaxFlows,
		RateLimitPPS:   r.Light.RateLimitPPS,
		DSCPFixed:      uint8(r.Light.DSCPFixed),
		TTL:            r.TTL,
		MaxPacketSize:  r.MaxPacketSize,
		Timestamping:   timestampingMode(cfg.Daemon.Timestamping),
		UserspaceError: cfg.Daemon.Clock.UserspaceError,
		KernelError:    cfg.Daemon.Clock.KernelError,
		// Light takes RFC 7750 from configuration (RFC 7750 §2.2.3).
		DSCPECNMonitoring: r.Light.DSCPECNMonitoring,
		STAMP:             r.Light.STAMP,
	}
	if r.Light.Sequence == "copy_sender" {
		out.Sequence = reflector.SequenceCopySender
	}
	if r.Light.DSCPPolicy == "fixed" {
		out.DSCPPolicy = reflector.DSCPFixed
	}
	for _, l := range r.Light.Listeners {
		ap, err := config.ParseAddrPort(l.Listen)
		if err != nil {
			return reflector.Config{}, fmt.Errorf("reflector.light.listeners: %w", err)
		}
		out.Listeners = append(out.Listeners, ap)
	}
	for _, s := range r.Light.ACL {
		p, err := config.ParsePrefix(s)
		if err != nil {
			return reflector.Config{}, fmt.Errorf("reflector.light.acl: %w", err)
		}
		out.ACL = append(out.ACL, p)
	}
	return out, nil
}

// serve reflects until ctx is done, reporting failures to fail.
//
//declscope:package // Start serves the reflector role with it
func (r *reflectors) serve(ctx context.Context, wg *sync.WaitGroup, fail func(error)) {
	if r == nil {
		return
	}
	if r.light != nil {
		wg.Go(func() {
			if err := r.light.Serve(ctx); err != nil {
				fail(fmt.Errorf("light reflector: %w", err))
			}
		})
	}
	if r.full != nil {
		wg.Go(func() {
			if err := r.full.Serve(ctx); err != nil {
				fail(fmt.Errorf("twamp-control: %w", err))
			}
		})
	}
}

// close releases the sockets of a reflector role that will not be served.
//
//declscope:package // Start releases them when a later step fails
func (r *reflectors) close() {
	if r == nil {
		return
	}
	if r.light != nil {
		_ = r.light.Close()
	}
	if r.full != nil {
		_ = r.full.Close()
	}
}

// lightAddrs returns the bound addresses of the Light reflector.
//
//declscope:package // Daemon.LightAddrs reports them
func (r *reflectors) lightAddrs() []netip.AddrPort {
	if r == nil || r.light == nil {
		return nil
	}
	return r.light.Addrs()
}

// controlAddrs returns the bound addresses of the TWAMP-Control server.
//
//declscope:package // Daemon.ControlAddrs reports them
func (r *reflectors) controlAddrs() []netip.AddrPort {
	if r == nil || r.full == nil {
		return nil
	}
	return r.full.Addrs()
}

// timestamping returns the timestamping mode of the reflector's sockets,
// or false when no socket reports one.
//
//declscope:package // GetStatus and the exporter report it
func (r *reflectors) timestamping() (tstamp.Mode, bool) {
	if r == nil || r.light == nil {
		return 0, false
	}
	ls := r.light.Status().Listeners
	if len(ls) == 0 {
		return 0, false
	}
	return ls[0].Timestamping, true
}

// timestampingInEffect returns the timestamping mode of the daemon's
// sockets: that of the Light reflector's, or else that of a probe socket
// opened in the configured mode (SPEC §8.1). A hardware probe binds the
// wildcard address on daemon.hardware_interface, iface, since the loopback
// device has no card.
//
//declscope:package // Start reports it in GetStatus and the metrics
func timestampingInEffect(r *reflectors, configured, iface string) tstamp.Mode {
	if m, ok := r.timestamping(); ok {
		return m
	}
	opts := tstamp.Options{Mode: timestampingMode(configured)}
	probes := []struct {
		network string
		addr    netip.AddrPort
	}{{"udp4", netip.MustParseAddrPort("127.0.0.1:0")}, {"udp6", netip.MustParseAddrPort("[::1]:0")}}
	if opts.Mode == tstamp.ModeHardware && iface != "" {
		opts.Interface = iface
		probes[0].addr, probes[1].addr = netip.MustParseAddrPort("0.0.0.0:0"), netip.MustParseAddrPort("[::]:0")
	}
	for _, probe := range probes {
		c, err := tstamp.Listen(probe.network, probe.addr, opts)
		if err != nil {
			continue
		}
		m := c.Mode()
		_ = c.Close()
		return m
	}
	return tstamp.ModeUserspace
}

// GetReflectorStatus implements gotwamp.v1.ReflectorService.
func (r *reflectors) GetReflectorStatus(context.Context, *gotwampv1.GetReflectorStatusRequest) (*gotwampv1.GetReflectorStatusResponse, error) {
	resp := &gotwampv1.GetReflectorStatusResponse{Enabled: r.enabled()}
	if r.light != nil {
		st := r.light.Status()
		for _, l := range st.Listeners {
			resp.Listeners = append(resp.Listeners, &gotwampv1.ReflectorListener{
				Kind:             gotwampv1.ReflectorKind_REFLECTOR_KIND_LIGHT,
				Address:          l.Address.String(),
				PacketsReceived:  l.Counters.Received,
				PacketsReflected: l.Counters.Reflected,
			})
		}
		resp.LightFlowsActive = count32(st.FlowsActive)
		resp.LightFlowsOverflow = st.FlowsOverflow
		resp.Counters = append(resp.Counters, protoCounters(gotwampv1.ReflectorKind_REFLECTOR_KIND_LIGHT, st.Counters))
	}
	if r.full != nil {
		fullStatus := r.full.Status()
		for _, a := range fullStatus.Listeners {
			resp.Listeners = append(resp.Listeners, &gotwampv1.ReflectorListener{Kind: gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL, Address: a.String()})
		}
		st := fullStatus.Control
		resp.ControlConnectionsActive = count32(st.ConnectionsActive)
		resp.ControlConnectionsAccepted = st.ConnectionsAccepted
		resp.ControlConnectionsRejected = st.ConnectionsRejected
		resp.FullSessionsActive = count32(st.SessionsActive)
		resp.Counters = append(resp.Counters, protoCounters(gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL, fullStatus.Counters))
	}
	return resp, nil
}

func (r *reflectors) enabled() bool { return r != nil && !r.disabled }

// reflectorService returns the ReflectorService of r, which may be nil.
//
//declscope:package // Start registers it
func reflectorService(r *reflectors) *reflectors {
	if r == nil {
		return &reflectors{disabled: true}
	}
	return r
}

func protoCounters(kind gotwampv1.ReflectorKind, c reflector.Counters) *gotwampv1.ReflectorCounters {
	out := &gotwampv1.ReflectorCounters{Kind: kind, PacketsReceived: c.Received, PacketsReflected: c.Reflected}
	for _, reason := range reflector.DropReasons() {
		out.Dropped = append(out.Dropped, &gotwampv1.DropCount{Reason: protoDropReason(reason), Count: c.Dropped[reason]})
	}
	return out
}

// protoDropReason maps a reflector drop reason to the API enum by its metric
// label ("acl" is DROP_REASON_ACL).
func protoDropReason(r reflector.DropReason) gotwampv1.DropReason {
	return gotwampv1.DropReason(gotwampv1.DropReason_value["DROP_REASON_"+strings.ToUpper(r.String())])
}

// ListReflectorSessions implements gotwamp.v1.ReflectorService.
func (r *reflectors) ListReflectorSessions(_ context.Context, req *gotwampv1.ListReflectorSessionsRequest) (*gotwampv1.ListReflectorSessionsResponse, error) {
	if err := checkReflectorKind(req.GetKind()); err != nil {
		return nil, err
	}
	resp := &gotwampv1.ListReflectorSessionsResponse{}
	if r == nil {
		return resp, nil
	}
	kind := req.GetKind()
	if r.full != nil && kind != gotwampv1.ReflectorKind_REFLECTOR_KIND_LIGHT {
		for _, s := range r.full.Sessions() {
			resp.FullSessions = append(resp.FullSessions, &gotwampv1.FullReflectorSession{
				Sid:              s.SID[:],
				ControlPeer:      protoEndpoint(s.ControlPeer),
				Sender:           protoEndpoint(s.Sender),
				Reflector:        protoEndpoint(s.Reflector),
				State:            gotwampv1.FullSessionState(s.State), //nolint:gosec // G115: the states share their numbers
				Padding:          s.Padding,
				Dscp:             uint32(s.DSCP),
				Timeout:          durationpb.New(s.Timeout),
				AcceptedAt:       timestamppb.New(s.AcceptedAt),
				PacketsReceived:  s.Stats.Received,
				PacketsReflected: s.Stats.Reflected,
				LastReceivedAt:   optTimestamp(s.Stats.LastReceived),
				Security:         sessionSecurity(s.Security),
			})
		}
	}
	if r.light != nil && kind != gotwampv1.ReflectorKind_REFLECTOR_KIND_FULL {
		for _, f := range r.light.Flows() {
			resp.LightFlows = append(resp.LightFlows, &gotwampv1.LightFlow{
				Source:           protoEndpoint(f.Source),
				Destination:      protoEndpoint(f.Destination),
				NextSeq:          f.NextSeq,
				PacketsReceived:  f.Received,
				PacketsReflected: f.Reflected,
				FirstReceivedAt:  timestamppb.New(f.FirstReceived),
				LastReceivedAt:   timestamppb.New(f.LastReceived),
			})
		}
	}
	return resp, nil
}

// sessionSecurity converts the security mode of a control connection.
func sessionSecurity(m wire.SecurityMode) gotwampv1.SecurityMode {
	switch m {
	case wire.SecurityUnauthenticated:
		return gotwampv1.SecurityMode_SECURITY_MODE_UNAUTHENTICATED
	case wire.SecurityAuthenticated:
		return gotwampv1.SecurityMode_SECURITY_MODE_AUTHENTICATED
	case wire.SecurityEncrypted:
		return gotwampv1.SecurityMode_SECURITY_MODE_ENCRYPTED
	case wire.SecurityMixed:
		return gotwampv1.SecurityMode_SECURITY_MODE_MIXED
	default:
		return gotwampv1.SecurityMode_SECURITY_MODE_UNSPECIFIED
	}
}

// snapshot reports the reflector counters to the Prometheus exporter.
//
//declscope:package // Start hands it to the exporter
func (r *reflectors) snapshot() prom.ReflectorSnapshot {
	var s prom.ReflectorSnapshot
	if r == nil {
		return s
	}
	s.SessionsActive = map[string]int{}
	drops := func(kind string, c reflector.Counters) {
		for _, reason := range reflector.DropReasons() {
			s.Dropped = append(s.Dropped, prom.ReflectorDrops{Kind: kind, Reason: reason.String(), Count: c.Dropped[reason]})
		}
	}
	if r.light != nil {
		st := r.light.Status()
		for _, l := range st.Listeners {
			s.Listeners = append(s.Listeners, prom.ReflectorListener{Kind: "light", Listener: l.Address.String(), Received: l.Counters.Received, Reflected: l.Counters.Reflected})
		}
		drops("light", st.Counters)
		s.SessionsActive["light"] = st.FlowsActive
		s.LightFlowsOverflow = st.FlowsOverflow
	}
	if r.full != nil {
		fullStatus := r.full.Status()
		c := fullStatus.Counters
		// Full sessions have their own sockets; their counters are summed
		// under the control listeners' addresses (B28).
		addrs := make([]string, 0, len(fullStatus.Listeners))
		for _, a := range fullStatus.Listeners {
			addrs = append(addrs, a.String())
		}
		s.Listeners = append(s.Listeners, prom.ReflectorListener{Kind: "full", Listener: strings.Join(addrs, ","), Received: c.Received, Reflected: c.Reflected})
		drops("full", c)
		st := fullStatus.Control
		s.ControlConnectionsActive = st.ConnectionsActive
		s.ControlConnections = map[string]uint64{"accepted": st.ConnectionsAccepted, "rejected": st.ConnectionsRejected}
		s.SessionsActive["full"] = st.SessionsActive
	}
	return s
}

// protoEndpoint converts an address and UDP port.
//
//declscope:package // the sender service reports endpoints too
func protoEndpoint(ap netip.AddrPort) *gotwampv1.Endpoint {
	return &gotwampv1.Endpoint{Address: ap.Addr().String(), Port: uint32(ap.Port())}
}

// timestampingMode parses daemon.timestamping.
//
//declscope:package // test plans use the same mode
func timestampingMode(s string) tstamp.Mode {
	switch s {
	case "hardware":
		return tstamp.ModeHardware
	case "kernel":
		return tstamp.ModeKernel
	case "userspace":
		return tstamp.ModeUserspace
	default:
		return tstamp.ModeAuto
	}
}
