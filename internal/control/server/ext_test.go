//declscope:namespace server

package server_test

import (
	"errors"
	"testing"
	"testing/synctest"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// extHarness offers the unauthenticated mode with every extension.
func extHarness(t *testing.T) *harness {
	return newHarness(t, server.Config{Modes: wire.ModeUnauthenticated | server.FeatureModes})
}

// stateOf returns the server's state of session sid.
func stateOf(t *testing.T, hs *harness, sid wire.SID) server.SessionState {
	t.Helper()
	for _, s := range hs.s.Sessions() {
		if s.SID == sid {
			return s.State
		}
	}
	return 0
}

// TestIndividualSessionControl runs sessions that start and stop one by one
// on one connection (RFC 5938), requesting a new one while others run.
func TestIndividualSessionControl(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := extHarness(t)
		conn, done := hs.dial(clientAddr)
		c, err := client.Handshake(t.Context(), conn, client.Config{Features: wire.ModeIndividualSessionControl})
		if err != nil {
			t.Fatal(err)
		}
		if c.Features() != wire.ModeIndividualSessionControl {
			t.Fatalf("features %v", c.Features())
		}
		req := func(port uint16) *client.Session {
			t.Helper()
			s, err := c.Request(t.Context(), client.Request{SenderPort: port})
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		s1, s2 := req(50001), req(50002)
		if ok, err := c.StartN(t.Context(), []*client.Session{s1}); err != nil || len(ok) != 1 {
			t.Fatalf("StartN = %v, %v", ok, err)
		}
		if stateOf(t, hs, s1.SID) != server.SessionActive || stateOf(t, hs, s2.SID) != server.SessionAccepted || c.State() != client.StateActive {
			t.Errorf("after starting s1: %+v, client %v", hs.s.Sessions(), c.State())
		}
		// A new session while s1 runs, then both others start together.
		s3 := req(50003)
		if ok, err := c.StartN(t.Context(), []*client.Session{s2, s3}); err != nil || len(ok) != 2 {
			t.Fatalf("StartN = %v, %v", ok, err)
		}
		if ok, err := c.StopN(t.Context(), []*client.Session{s1}); err != nil || len(ok) != 1 {
			t.Fatalf("StopN = %v, %v", ok, err)
		}
		if stateOf(t, hs, s1.SID) != server.SessionDraining || stateOf(t, hs, s2.SID) != server.SessionActive || c.State() != client.StateActive {
			t.Errorf("after stopping s1: %+v", hs.s.Sessions())
		}
		// Starting a stopped session is refused; the connection stays.
		if ok, err := c.StartN(t.Context(), []*client.Session{s1}); err == nil || len(ok) != 0 {
			t.Errorf("restarting s1: %v, %v", ok, err)
		}
		var ce *client.Error
		if _, err := c.StartN(t.Context(), []*client.Session{s1}); !errors.As(err, &ce) || ce.Stage != client.StageStart {
			t.Errorf("restarting s1 again: %v", err)
		}
		if ok, err := c.StopN(t.Context(), []*client.Session{s2, s3}); err != nil || len(ok) != 2 {
			t.Fatalf("StopN = %v, %v", ok, err)
		}
		if c.State() != client.StateIdle {
			t.Errorf("client state %v after every session stopped", c.State())
		}
		// The RFC 5357 commands are not for this connection.
		if err := c.Start(t.Context()); !errors.Is(err, client.ErrState) {
			t.Errorf("Start under individual session control: %v", err)
		}
		_ = c.Close()
		<-done
	})
}

// TestIndividualSessionControlStopAccepted stops a session that never
// started: the server forgets it.
func TestIndividualSessionControlStopAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := extHarness(t)
		conn, done := hs.dial(clientAddr)
		c, err := client.Handshake(t.Context(), conn, client.Config{Features: server.FeatureModes})
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.Request(t.Context(), client.Request{SenderPort: 50000})
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := c.StopN(t.Context(), []*client.Session{s}); err != nil || len(ok) != 1 {
			t.Fatalf("StopN = %v, %v", ok, err)
		}
		if n := hs.s.Status().SessionsActive; n != 0 {
			t.Errorf("%d sessions left", n)
		}
		if _, err := c.StartN(t.Context(), []*client.Session{s}); !errors.Is(err, client.ErrState) {
			t.Errorf("starting a stopped session: %v", err)
		}
		_ = c.Close()
		<-done
	})
}

// TestIndividualSessionControlLost checks that the client notices the
// server going away while sessions run under individual session control.
func TestIndividualSessionControlLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := extHarness(t)
		conn, done := hs.dial(clientAddr)
		c, err := client.Handshake(t.Context(), conn, client.Config{Features: wire.ModeIndividualSessionControl})
		if err != nil {
			t.Fatal(err)
		}
		s, _ := c.Request(t.Context(), client.Request{SenderPort: 50000})
		if _, err := c.StartN(t.Context(), []*client.Session{s}); err != nil {
			t.Fatal(err)
		}
		lost := c.Lost()
		_ = conn.Close() // stands for the network failing
		<-lost
		if !errors.Is(c.Err(), client.ErrControlLost) {
			t.Errorf("Err = %v", c.Err())
		}
		if _, err := c.StopN(t.Context(), []*client.Session{s}); !errors.Is(err, client.ErrControlLost) {
			t.Errorf("StopN after the loss: %v", err)
		}
		<-done
	})
}

// TestIndividualSessionControlRaw sends what the client package never does.
func TestIndividualSessionControlRaw(t *testing.T) {
	isc := (&wire.SetUpResponse{Mode: wire.ModeUnauthenticated | wire.ModeIndividualSessionControl}).Append(nil)
	start := func(t *testing.T, hs *harness) (raw, <-chan struct{}) {
		conn, done := hs.dial(clientAddr)
		r := raw{t, conn}
		r.recv(wire.ServerGreetingSize)
		r.send(isc)
		r.recv(wire.ServerStartSize)
		return r, done
	}
	for _, tt := range []struct {
		name string
		msg  []byte
	}{
		{"start-sessions", (&wire.StartSessions{}).Append(nil)},
		{"stop-sessions", (&wire.StopSessions{}).Append(nil)},
		{"start-n for no session", (&wire.SessionControl{Command: wire.CommandStartNSessions}).Append(nil)},
		{"stop-n for more sessions than a connection has", (&wire.SessionControl{Command: wire.CommandStopNSessions, SIDs: make([]wire.SID, 17)}).Append(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, done := start(t, extHarness(t))
				// The server may close before reading the whole message.
				go func() { _, _ = r.conn.Write(tt.msg) }()
				<-done // a protocol violation closes the connection
				if !r.closed() {
					t.Error("connection still open")
				}
			})
		})
	}
	t.Run("unknown sid", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r, done := start(t, extHarness(t))
			r.send((&wire.SessionControl{Command: wire.CommandStartNSessions, SIDs: []wire.SID{{9}}}).Append(nil))
			var ack wire.SessionControl
			if err := ack.Decode(r.recv(wire.SessionControlSize(1))); err != nil || ack.Command != wire.CommandStartNAck || ack.Accept != wire.AcceptFailure || ack.SIDs[0] != (wire.SID{9}) {
				t.Errorf("ack %+v, %v", ack, err)
			}
			_ = r.conn.Close()
			<-done
		})
	})
	t.Run("not negotiated", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			r, done := extHarness(t).rawHandshake()
			// The server stops reading after the header.
			go func() {
				_, _ = r.conn.Write((&wire.SessionControl{Command: wire.CommandStartNSessions, SIDs: []wire.SID{{9}}}).Append(nil))
			}()
			var as wire.AcceptSession
			_ = as.Decode(r.recv(wire.AcceptSessionSize))
			if as.Accept != wire.AcceptNotSupported {
				t.Errorf("answer %+v, want Accept-Session 3 (RFC 5357 §3.5)", as)
			}
			<-done
		})
	})
}

// TestReflectOctetsAndSymmetricalSize negotiates the RFC 6038 modes: the
// server returns the octets to be reflected and hands the layout to the
// reflector; a padding too short for it is refused with Accept 3.
func TestReflectOctetsAndSymmetricalSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := extHarness(t)
		conn, done := hs.dial(clientAddr)
		c, err := client.Handshake(t.Context(), conn, client.Config{Features: wire.ModeReflectOctets | wire.ModeSymmetricalSize})
		if err != nil {
			t.Fatal(err)
		}
		s, err := c.Request(t.Context(), client.Request{SenderPort: 50000, PaddingLength: 64, OctetsToReflect: 0xbeef, PaddingToReflect: 8})
		if err != nil {
			t.Fatal(err)
		}
		want := wire.PaddingLayout{SymmetricalSize: true, ReflectLength: 8}
		if spec := hs.h.session(0).spec; spec.Layout != want || s.Layout != want || s.ServerOctets != 0 {
			t.Errorf("layout server %+v client %+v", spec.Layout, s.Layout)
		}
		// The client refuses a padding the layout cannot hold before sending.
		if _, err := c.Request(t.Context(), client.Request{SenderPort: 50001, PaddingLength: 8, PaddingToReflect: 8}); err == nil || c.State() != client.StateIdle {
			t.Errorf("padding 8 reflecting 8: %v", err)
		}
		_ = c.Close()
		<-done
	})
	t.Run("server checks", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hs := extHarness(t)
			conn, done := hs.dial(clientAddr)
			r := raw{t, conn}
			r.recv(wire.ServerGreetingSize)
			r.send((&wire.SetUpResponse{Mode: wire.ModeUnauthenticated | wire.ModeReflectOctets}).Append(nil))
			r.recv(wire.ServerStartSize)
			req := validRequest()
			req.PaddingLength, req.OctetsToReflect, req.PaddingToReflect = 34, 0x1234, 8 // below 27 + 8
			as := r.request(req)
			if as.Accept != wire.AcceptNotSupported || as.ReflectedOctets != 0x1234 {
				t.Errorf("accept-session %+v, want Accept 3 with the octets", as)
			}
			req.PaddingLength = 35
			if as := r.request(req); as.Accept != wire.AcceptOK || as.ReflectedOctets != 0x1234 {
				t.Errorf("accept-session %+v", as)
			}
			_ = conn.Close()
			<-done
		})
	})
	t.Run("not offered", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, server.Config{})
			conn, done := hs.dial(clientAddr)
			c, err := client.Handshake(t.Context(), conn, client.Config{Features: wire.ModeReflectOctets})
			if err != nil || c.Features() != 0 {
				t.Fatalf("features %v, %v", c.Features(), err)
			}
			if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000, PaddingLength: 64, PaddingToReflect: 8}); err == nil {
				t.Error("reflect octets used without the mode")
			}
			_ = c.Close()
			<-done
		})
	})
}
