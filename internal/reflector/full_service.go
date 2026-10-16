package reflector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
)

// PortRange is an inclusive range of UDP test ports.
type PortRange = server.PortRange

// ControlStatus reports connection and session counts.
type ControlStatus = server.Status

// SessionInfo is a snapshot of a Full test session.
type SessionInfo = server.SessionInfo

// SessionState is the lifecycle state of a Full test session.
type SessionState = server.SessionState

// SessionStats reports the packet counts and last arrival of a Full session.
type SessionStats = server.SessionStats

const (
	// SessionAccepted means a test socket is bound and awaits Start-Sessions.
	SessionAccepted = server.SessionAccepted
	// SessionActive means the session reflects test packets.
	SessionActive = server.SessionActive
	// SessionDraining means the session reflects until its stop timeout expires.
	SessionDraining = server.SessionDraining
)

// controlConfig maps the curated configuration to the control engine.
//
//declscope:package // FullConfig defaults and validation use the same mapping
func (c FullConfig) controlConfig() server.Config {
	return server.Config{
		ACL: c.ACL, MaxConnections: c.MaxConnections, MaxSessionsPerConnection: c.MaxSessionsPerConnection,
		MaxSessions: c.MaxSessions, ServWait: c.ServWait, RefWait: c.RefWait,
		PartialMessageTimeout: c.PartialMessageTimeout, TestPorts: c.TestPorts,
		IgnoreRequestedPort: c.IgnoreRequestedPort, MaxPadding: c.MaxPadding,
		Count: c.Count, Modes: c.Modes, Keys: c.Credentials,
	}
}

// FullStatus is a snapshot of a complete Full reflector service.
type FullStatus struct {
	// Listeners are the bound TCP control addresses in configuration order.
	Listeners []netip.AddrPort
	// Control reports control connections and test sessions.
	Control ControlStatus
	// Counters sum packet counters across all current and completed sessions.
	Counters Counters
}

// FullService owns a TWAMP-Control server, its TCP listeners and UDP sessions.
// ListenFull constructs it; Serve starts the control workers.
type FullService struct {
	engine    *Full
	control   *server.Server
	listeners []net.Listener
	life      serviceLife
}

// ListenFull validates cfg before binding its control listeners. If any bind
// fails, every previously opened listener is closed before returning.
func ListenFull(cfg FullConfig, deps Deps) (*FullService, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	engine, err := NewFull(cfg, deps)
	if err != nil {
		return nil, err
	}
	log := deps.ControlLogger
	if log == nil {
		log = deps.Logger
	}
	control, err := server.New(cfg.controlConfig(), engine, log)
	if err != nil {
		return nil, err
	}
	f := &FullService{engine: engine, control: control}
	listen := deps.ListenControl
	if listen == nil {
		listen = ListenControl
	}
	for _, addr := range cfg.Listeners {
		network := "tcp6"
		if addr.Addr().Unmap().Is4() {
			network = "tcp4"
			addr = netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
		}
		ln, err := listen(network, addr)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("full reflector: listen %v: %w", addr, err), f.Close())
		}
		f.listeners = append(f.listeners, ln)
	}
	return f, nil
}

// ListenControl binds a control listener with the protocol's TCP keepalive.
// Internal callers may use it when supplying their own listener dependency.
func ListenControl(network string, addr netip.AddrPort) (net.Listener, error) {
	lc := net.ListenConfig{KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 120 * time.Second, Interval: 5 * time.Second, Count: 6}}
	ln, err := lc.Listen(context.Background(), network, addr.String())
	if err != nil {
		return nil, fmt.Errorf("control listener: %w", err)
	}
	return ln, nil
}

// Serve handles control connections and test sessions until cancellation or
// Close. It may be called once. Normal cancellation returns nil. A permanent
// accept error stops the whole service and is returned after all workers stop.
func (f *FullService) Serve(ctx context.Context) error {
	ctx, finish, err := f.life.begin(ctx)
	if err != nil {
		return fmt.Errorf("full reflector: %w", err)
	}
	defer finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make([]error, len(f.listeners))
	var wg sync.WaitGroup
	for i, ln := range f.listeners {
		wg.Go(func() {
			errs[i] = f.control.Serve(ctx, ln)
			if errs[i] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	return errors.Join(errors.Join(errs...), f.life.shutdown(f.closeSockets))
}

// Close releases listeners and joins every control and session worker.
// It is safe before Serve and may be called concurrently or repeatedly.
func (f *FullService) Close() error {
	err := f.life.shutdown(f.closeSockets)
	f.life.wait()
	return err
}

func (f *FullService) closeSockets() error {
	var errs []error
	for _, ln := range f.listeners {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("full reflector: close %v: %w", ln.Addr(), err))
		}
	}
	return errors.Join(errs...)
}

// Addrs returns bound control addresses in configuration order.
func (f *FullService) Addrs() []netip.AddrPort {
	out := make([]netip.AddrPort, len(f.listeners))
	for i, ln := range f.listeners {
		out[i] = ln.Addr().(*net.TCPAddr).AddrPort()
	}
	return out
}

// Status reports control and packet counters, including completed sessions.
func (f *FullService) Status() FullStatus {
	return FullStatus{Listeners: f.Addrs(), Control: f.control.Status(), Counters: f.engine.Counters()}
}

// Sessions returns an unordered snapshot of sessions not yet released.
func (f *FullService) Sessions() []SessionInfo { return f.control.Sessions() }
