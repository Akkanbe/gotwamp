//declscope:namespace server

package server

import (
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// This file is the server side of the RFC 6038 and RFC 5938 extensions
// (SPEC §13): the padding layout of a request, and starting and stopping
// sessions by SID.

// FeatureModes are the Modes bits beyond the security modes that the server
// can offer: Individual Session Control (RFC 5938), Reflect Octets and
// Symmetrical Size (RFC 6038).
const FeatureModes = wire.ModeIndividualSessionControl | wire.ModeReflectOctets | wire.ModeSymmetricalSize

// layout returns the RFC 6038 layout of the test packets r asks for under
// the modes negotiated. Without Reflect Octets, offsets 88–91 are MBZ and
// ignored.
func (c *conn) layout(r *wire.RequestTWSession) wire.PaddingLayout {
	l := wire.PaddingLayout{SymmetricalSize: c.features.Has(wire.ModeSymmetricalSize)}
	if c.features.Has(wire.ModeReflectOctets) {
		l.ReflectLength = r.PaddingToReflect
	}
	return l
}

// handleStartN answers Start-N-Sessions (RFC 5938 §3.2, §3.3): the listed
// sessions that are accepted and not started begin, and are acknowledged
// with Accept 0; the others, unknown or already started, in a second
// Start-N-Ack with Accept 1.
func (c *conn) handleStartN(b []byte) error {
	sids, err := c.sessionList(b)
	if err != nil {
		return err
	}
	now := c.s.cfg.Now()
	var ok, failed []wire.SID
	for _, sid := range sids {
		if ss := c.find(sid); ss != nil && ss.info.State == SessionAccepted {
			c.start(ss, now)
			ok = append(ok, sid)
		} else {
			failed = append(failed, sid)
		}
	}
	if len(ok) > 0 {
		c.state = stateActive
	}
	return c.acknowledge(wire.CommandStartNAck, ok, failed)
}

// handleStopN answers Stop-N-Sessions (RFC 5938 §3.4, §3.5): the listed
// started sessions drain for their Timeout, accepted ones are released, and
// unknown or already stopped ones are refused with Accept 1. SERVWAIT
// resumes once no session of the connection runs.
func (c *conn) handleStopN(b []byte) error {
	sids, err := c.sessionList(b)
	if err != nil {
		return err
	}
	now := c.s.cfg.Now()
	var ok, failed []wire.SID
	for _, sid := range sids {
		switch ss := c.find(sid); {
		case ss == nil, ss.info.State == SessionDraining:
			failed = append(failed, sid)
			continue
		case ss.info.State == SessionActive:
			c.drain(ss, now)
		default: // accepted, never started
			c.s.release(ss)
		}
		ok = append(ok, sid)
	}
	if c.running() == 0 {
		c.state, c.lastActivity = stateRequestWait, now
	}
	return c.acknowledge(wire.CommandStopNAck, ok, failed)
}

// sessionList decodes Start-N-Sessions or Stop-N-Sessions. A message naming
// no session is a protocol violation (Number of Sessions ≥ 1).
func (c *conn) sessionList(b []byte) ([]wire.SID, error) {
	var m wire.SessionControl
	if err := m.Decode(b); err != nil {
		return nil, fmt.Errorf("%w: %w", errProtocol, err)
	}
	if len(m.SIDs) == 0 {
		return nil, fmt.Errorf("%w: %v for no session", errProtocol, m.Command)
	}
	return m.SIDs, nil
}

// acknowledge sends the Start-N-Ack or Stop-N-Ack of ok sessions with
// Accept 0 and of failed ones with Accept 1, one message per outcome as
// RFC 5938 §3.3 allows.
func (c *conn) acknowledge(cmd wire.Command, ok, failed []wire.SID) error {
	for _, part := range []struct {
		accept wire.Accept
		sids   []wire.SID
	}{{wire.AcceptOK, ok}, {wire.AcceptFailure, failed}} {
		if len(part.sids) == 0 {
			continue
		}
		if part.accept != wire.AcceptOK {
			c.s.log.Info("control server: sessions refused", "peer", c.peer, "command", cmd.String(), "sessions", len(part.sids))
		}
		msg := wire.SessionControl{Command: cmd, Accept: part.accept, SIDs: part.sids}
		if err := c.send(msg.Append(c.buf[:0])); err != nil {
			return err
		}
	}
	return nil
}

// find returns the live session sid of the connection, or nil.
func (c *conn) find(sid wire.SID) *session {
	for _, ss := range c.live() {
		if ss.info.SID == sid {
			return ss
		}
	}
	return nil
}

// running returns how many sessions of the connection are started and not
// stopped.
func (c *conn) running() int {
	n := 0
	for _, ss := range c.live() {
		if ss.info.State == SessionActive {
			n++
		}
	}
	return n
}
