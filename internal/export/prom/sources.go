package prom

import "time"

// ClockSource reports the local clock state; *clock.Monitor satisfies it.
type ClockSource interface {
	Status() (synced bool, maxErr time.Duration)
}

// ReflectorSource reports the reflector counters at scrape time. The daemon
// adapts the reflector and the control server to it.
type ReflectorSource interface {
	ReflectorSnapshot() ReflectorSnapshot
}

// ReflectorSourceFunc adapts a function to ReflectorSource.
type ReflectorSourceFunc func() ReflectorSnapshot

// ReflectorSnapshot implements ReflectorSource.
func (f ReflectorSourceFunc) ReflectorSnapshot() ReflectorSnapshot { return f() }

// ReflectorSnapshot is the state of the reflector role.
type ReflectorSnapshot struct {
	// Listeners are the packet counters per listener.
	Listeners []ReflectorListener
	// Dropped are the discarded packets per kind and reason.
	Dropped []ReflectorDrops
	// ControlConnectionsActive is the number of open TWAMP-Control
	// connections.
	ControlConnectionsActive int
	// ControlConnections counts TWAMP-Control connections by result.
	ControlConnections map[string]uint64
	// SessionsActive is the number of Full sessions ("full") and Light
	// flows ("light").
	SessionsActive map[string]int
	// LightFlowsOverflow counts packets of new Light flows handled
	// statelessly because the flow table was full.
	LightFlowsOverflow uint64
}

// ReflectorListener holds the counters of one listener.
type ReflectorListener struct {
	// Kind is "full" or "light".
	Kind string
	// Listener is the bound address and port.
	Listener string
	// Received counts the datagrams read.
	Received uint64
	// Reflected counts the replies sent.
	Reflected uint64
}

// ReflectorDrops counts the packets dropped for one reason.
type ReflectorDrops struct {
	// Kind is "full" or "light".
	Kind string
	// Reason is acl, malformed, unknown_session, rate_limit, not_started
	// or drained.
	Reason string
	// Count is the number of packets.
	Count uint64
}
