package daemon

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
)

// socketOpener opens the daemon's test sockets. With timestamping hardware
// it names the interface whose card timestamps a socket bound to the
// wildcard address (daemon.hardware_interface, or the route to a Light
// sender's target), and logs once per interface and outcome whether the card
// timestamps or why the socket fell back to kernel timestamps.
//
//declscope:package // Start shares one among the reflector role and the runner
type socketOpener struct {
	// iface is daemon.hardware_interface.
	iface string
	log   *slog.Logger
	// reported holds the outcomes already logged.
	//
	//declscope:private
	reported sync.Map
}

// listen is tstamp.Listen, as the reflector role and Full runs open sockets.
//
//declscope:package // the reflectors' Deps.Listen and the Full runs' Listen
func (o *socketOpener) listen(network string, local netip.AddrPort, opts tstamp.Options) (tstamp.Conn, error) {
	return o.listenTo(network, local, opts, netip.Addr{})
}

// listenTo is listen for a socket that sends to target, whose route names
// the interface when none is configured.
//
//declscope:package // Light runs open their sockets with it
func (o *socketOpener) listenTo(network string, local netip.AddrPort, opts tstamp.Options, target netip.Addr) (tstamp.Conn, error) {
	if o == nil {
		return tstamp.Listen(network, local, opts)
	}
	if opts.Mode == tstamp.ModeHardware && opts.Interface == "" && local.Addr().IsUnspecified() {
		opts.Interface = o.iface
		if opts.Interface == "" && target.IsValid() {
			if name, err := tstamp.RouteInterface(target); err == nil {
				opts.Interface = name
			}
		}
	}
	c, err := tstamp.Listen(network, local, opts)
	if err == nil && opts.Mode == tstamp.ModeHardware {
		o.report(c)
	}
	return c, err
}

// report logs the hardware timestamping of c the first time its interface
// and outcome are seen.
func (o *socketOpener) report(c tstamp.Conn) {
	hr, ok := c.(tstamp.HardwareReporter)
	if !ok {
		return
	}
	st := hr.Hardware()
	outcome := "ok"
	if st.Err != nil {
		outcome = st.Err.Error()
	}
	if _, seen := o.reported.LoadOrStore(st.Interface+"\x00"+outcome, true); seen {
		return
	}
	if st.Err != nil {
		o.log.Warn("hardware timestamping unavailable; using kernel timestamps", "interface", st.Interface, "error", st.Err)
		return
	}
	// phc2sys must keep this clock on CLOCK_REALTIME (SPEC §8.1).
	o.log.Info("hardware timestamping", "interface", st.Interface, "phc", fmt.Sprintf("/dev/ptp%d", st.PHC))
}
