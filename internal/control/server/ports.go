//declscope:namespace server

package server

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"syscall"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// PortRange is an inclusive range of UDP ports.
type PortRange struct {
	Low, High uint16
}

// defaultTestPorts is the range alternate Receiver Ports come from (SPEC §5.8).
var defaultTestPorts = PortRange{Low: 20000, High: 29999}

// size returns the number of ports in r.
func (r PortRange) size() int { return int(r.High) - int(r.Low) + 1 }

// wellKnownPort is TWAMP's registered port (RFC 8545), which clients may ask
// for although it is below 1024.
const wellKnownPort = 862

// honors reports whether the requested Receiver Port is tried first: it must
// be set, honoring must be on, and it must not be a privileged port other
// than 862 (the daemon holds CAP_NET_BIND_SERVICE, and an unauthenticated
// client should not make it take ports such as 123 or 161), unless it lies
// in the test range.
func (s *Server) honors(requested uint16) bool {
	r := s.cfg.TestPorts
	return requested != 0 && !s.cfg.IgnoreRequestedPort &&
		(requested == wellKnownPort || requested >= 1024 || (requested >= r.Low && requested <= r.High))
}

// openSession binds the session socket (SPEC §5.8): the requested port when
// honored and free, else a port of the test range scanned from a random
// start. It returns the bound address, or the Accept value to answer with
// on failure.
func (s *Server) openSession(spec SessionSpec, requested uint16) (Session, netip.AddrPort, wire.Accept, error) {
	honored := s.honors(requested)
	if honored {
		spec.Local = withPort(spec, requested)
		sess, err := s.handler.Open(spec)
		if err == nil {
			return sess, spec.Local, wire.AcceptOK, nil
		}
		if a := openFailure(err); a != wire.AcceptOK {
			return nil, netip.AddrPort{}, a, err
		}
	}
	r := s.cfg.TestPorts
	var b [2]byte
	if _, err := io.ReadFull(s.cfg.Rand, b[:]); err != nil {
		return nil, netip.AddrPort{}, wire.AcceptInternalError, fmt.Errorf("port choice: %w", err)
	}
	start := int(binary.BigEndian.Uint16(b[:])) % r.size()
	for i := range r.size() {
		port := uint16(int(r.Low) + (start+i)%r.size()) //nolint:gosec // within r by construction
		if honored && port == requested {
			continue
		}
		spec.Local = withPort(spec, port)
		sess, err := s.handler.Open(spec)
		if err == nil {
			return sess, spec.Local, wire.AcceptOK, nil
		}
		if a := openFailure(err); a != wire.AcceptOK {
			return nil, netip.AddrPort{}, a, err
		}
	}
	return nil, netip.AddrPort{}, wire.AcceptTemporaryLimit, errors.New("no free port in the test port range")
}

func withPort(spec SessionSpec, port uint16) netip.AddrPort {
	return netip.AddrPortFrom(spec.Local.Addr(), port)
}

// openFailure classifies an Open error: AcceptOK means try another port.
func openFailure(err error) wire.Accept {
	switch {
	case errors.Is(err, ErrPortInUse), errors.Is(err, syscall.EADDRINUSE), errors.Is(err, syscall.EACCES):
		return wire.AcceptOK
	case errors.Is(err, ErrAddressNotLocal), errors.Is(err, syscall.EADDRNOTAVAIL), errors.Is(err, ErrUnsupportedRequest):
		return wire.AcceptNotSupported
	default:
		return wire.AcceptInternalError
	}
}
