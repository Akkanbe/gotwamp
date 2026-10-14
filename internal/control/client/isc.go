//declscope:namespace client

package client

import (
	"context"
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// This file is the client side of RFC 5938 Individual Session Control and
// the RFC 6038 padding layout.

// isc reports whether Individual Session Control was negotiated.
func (c *Client) isc() bool { return c.features.Has(wire.ModeIndividualSessionControl) }

// layout returns the RFC 6038 layout of the test packets req asks for.
func (c *Client) layout(req *wire.RequestTWSession) wire.PaddingLayout {
	return wire.PaddingLayout{SymmetricalSize: c.features.Has(wire.ModeSymmetricalSize), ReflectLength: req.PaddingToReflect}
}

// StartN sends Start-N-Sessions for sessions, which must be accepted and not
// started, and reads the Start-N-Acks (RFC 5938 §3.2, §3.3). It needs
// Individual Session Control, under which Request, StartN and StopN may
// interleave freely. It returns the sessions the server started; when it
// refused some, the error is an *Error with their Accept, and the others
// still run. The client is ACTIVE while any session runs.
func (c *Client) StartN(ctx context.Context, sessions []*Session) ([]*Session, error) {
	if !c.isc() || c.state == StateClosed || len(sessions) == 0 || !contains(c.pending, sessions) {
		return nil, &Error{Stage: StageStart, Err: ErrState}
	}
	ok, err := c.exchangeN(ctx, StageStart, wire.CommandStartNSessions, sessions)
	c.pending = without(c.pending, ok)
	c.active = append(c.active, ok...)
	if len(ok) > 0 && c.state == StateIdle {
		c.activate()
	}
	return ok, err
}

// StopN sends Stop-N-Sessions for sessions, started or only accepted, and
// reads the Stop-N-Acks (RFC 5938 §3.4, §3.5). The server drains the started
// ones for their Timeout and forgets the others. It returns the sessions the
// server stopped; the client is IDLE again once none runs.
func (c *Client) StopN(ctx context.Context, sessions []*Session) ([]*Session, error) {
	if !c.isc() || c.state == StateClosed || len(sessions) == 0 || !contains(append(c.active[:len(c.active):len(c.active)], c.pending...), sessions) {
		return nil, &Error{Stage: StageStop, Err: ErrState}
	}
	ok, err := c.exchangeN(ctx, StageStop, wire.CommandStopNSessions, sessions)
	c.active = without(c.active, ok)
	c.pending = without(c.pending, ok)
	if len(c.active) == 0 && c.state == StateActive {
		c.state = StateIdle
	}
	return ok, err
}

// exchangeN sends cmd for sessions and reads acknowledgements until each
// session has one. It returns the sessions acknowledged with Accept 0, and
// an *Error for the first other Accept.
func (c *Client) exchangeN(ctx context.Context, stage Stage, cmd wire.Command, sessions []*Session) ([]*Session, error) {
	if err := c.pause(); err != nil {
		return nil, err
	}
	defer c.resume()
	msg := wire.SessionControl{Command: cmd}
	bySID := make(map[wire.SID]*Session, len(sessions))
	for _, s := range sessions {
		msg.SIDs = append(msg.SIDs, s.SID)
		bySID[s.SID] = s
	}
	if err := c.send(ctx, stage, msg.Append(nil)); err != nil {
		return nil, c.fail(err)
	}
	var ok []*Session
	var refused *Error
	for len(bySID) > 0 {
		ack, err := c.receiveN(ctx, stage, len(bySID))
		if err != nil {
			return ok, c.fail(err)
		}
		if ack.Command != cmd+1 { // the ack of 7 is 8, of 9 is 10
			return ok, c.fail(&Error{Stage: stage, Err: fmt.Errorf("%w: %v answering %v", ErrUnexpectedMessage, ack.Command, cmd)})
		}
		for _, sid := range ack.SIDs {
			s, found := bySID[sid]
			if !found {
				return ok, c.fail(&Error{Stage: stage, Err: fmt.Errorf("%w: %v for session %v, not asked or acknowledged twice", ErrUnexpectedMessage, ack.Command, sid)})
			}
			delete(bySID, sid)
			if ack.Accept == wire.AcceptOK {
				ok = append(ok, s)
			}
		}
		if ack.Accept != wire.AcceptOK && refused == nil {
			refused = rejected(stage, fmt.Sprintf("%v for %d of %d sessions", ack.Command, len(ack.SIDs), len(sessions)), ack.Accept)
		}
	}
	if refused != nil {
		return ok, refused
	}
	return ok, nil
}

// receiveN reads one Start-N-Ack or Stop-N-Ack listing at most most
// sessions: the header first, whose Number of Sessions gives the length.
func (c *Client) receiveN(ctx context.Context, stage Stage, most int) (wire.SessionControl, error) {
	var ack wire.SessionControl
	head := make([]byte, wire.ControlHeaderSize, wire.SessionControlSize(most))
	if err := c.read(ctx, stage, head); err != nil {
		return ack, err
	}
	if err := c.sec.Open(head); err != nil {
		return ack, &Error{Stage: stage, Err: err}
	}
	n := wire.SessionControlCount(head)
	if !wire.Command(head[0]).IsSessionControl() || n == 0 || n > uint32(most) { //nolint:gosec // G115: a count of sessions held in memory
		return ack, &Error{Stage: stage, Err: fmt.Errorf("%w: %v listing %d sessions", ErrUnexpectedMessage, wire.Command(head[0]), n)}
	}
	msg := head[:wire.SessionControlSize(int(n))]
	rest := msg[wire.ControlHeaderSize:]
	if err := c.read(ctx, stage, rest); err != nil {
		return ack, err
	}
	if err := c.sec.Open(rest); err != nil {
		return ack, &Error{Stage: stage, Err: err}
	}
	if err := c.sec.Verify(msg); err != nil {
		return ack, &Error{Stage: stage, Err: err}
	}
	if err := ack.Decode(msg); err != nil {
		return ack, &Error{Stage: stage, Err: err}
	}
	return ack, nil
}

// contains reports whether every session of want is in have.
func contains(have, want []*Session) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			found = found || h == w
		}
		if !found {
			return false
		}
	}
	return true
}

// without returns list minus the sessions of drop, in a new slice.
func without(list, drop []*Session) []*Session {
	var out []*Session
	for _, s := range list {
		if !contains(drop, []*Session{s}) {
			out = append(out, s)
		}
	}
	return out
}
