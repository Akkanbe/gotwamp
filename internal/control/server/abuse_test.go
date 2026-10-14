//declscope:namespace server

package server_test

import (
	"bytes"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Timers of the abuse tests, short so that a fuzz input runs through every
// one of them in little synthetic time.
const (
	abuseServWait = 10 * time.Second
	abuseRefWait  = 30 * time.Second
	abusePartial  = 5 * time.Second
)

func abuseConfig() server.Config {
	return server.Config{
		ServWait:                 abuseServWait,
		RefWait:                  abuseRefWait,
		PartialMessageTimeout:    abusePartial,
		MaxSessionsPerConnection: 4,
		MaxSessions:              8,
		MaxConnections:           4,
		Modes:                    allModes | server.FeatureModes,
		Keys:                     testKeys,
	}
}

// authSeed is a valid exchange in mode for a harness whose testRand makes
// the Challenge, Salt and Server-IV end in 1, 2 and 3: a Set-Up-Response
// for alice, then Request-TW-Session, Start-Sessions and Stop-Sessions
// sealed on the client's stream.
func authSeed(mode wire.SecurityMode) []byte {
	challenge, salt, serverIV := [16]byte{15: 1}, [16]byte{15: 2}, [16]byte{15: 3}
	keys := wire.SessionKeys{AES: [16]byte{1}, HMAC: [32]byte{2}}
	clientIV := [16]byte{9}
	key, _ := wire.DeriveKey([]byte("secretpass"), salt, wire.GreetingCountDefault)
	setup := wire.SetUpResponse{Mode: mode.Mode(), Token: wire.EncryptToken(key, challenge, keys), ClientIV: clientIV}
	setup.KeyID, _ = wire.KeyID("alice")
	cc := wire.NewControlCrypto(keys, clientIV, serverIV)
	out := setup.Append(nil)
	req := validRequest()
	for _, m := range []interface{ Append([]byte) []byte }{&req, &wire.StartSessions{}, &wire.StopSessions{Sessions: 1}} {
		b := m.Append(nil)
		_ = cc.Seal(b, true)
		out = append(out, b...)
	}
	return out
}

// abuseSeeds are client byte streams that walk the protocol, cut it short or
// break it.
func abuseSeeds() [][]byte {
	setup := (&wire.SetUpResponse{Mode: wire.ModeUnauthenticated}).Append(nil)
	req := validRequest()
	start := (&wire.StartSessions{}).Append(nil)
	stop := (&wire.StopSessions{Sessions: 1}).Append(nil)
	huge := validRequest()
	huge.Timeout = ntptime.New(0xffffffff, 0xffffffff)
	huge.Packets = 0xffffffff
	huge.PaddingLength = 0xffffffff
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	isc := (&wire.SetUpResponse{Mode: wire.ModeUnauthenticated | wire.ModeIndividualSessionControl}).Append(nil)
	startN := func(sids ...wire.SID) []byte {
		return (&wire.SessionControl{Command: wire.CommandStartNSessions, SIDs: sids}).Append(nil)
	}
	stopN := func(sids ...wire.SID) []byte {
		return (&wire.SessionControl{Command: wire.CommandStopNSessions, SIDs: sids}).Append(nil)
	}
	reflect := validRequest()
	reflect.PaddingLength, reflect.OctetsToReflect, reflect.PaddingToReflect = 40, 0xbeef, 8
	return [][]byte{
		nil,
		setup[:10],                          // partial Set-Up-Response
		(&wire.SetUpResponse{}).Append(nil), // Mode 0
		(&wire.SetUpResponse{Mode: wire.ModeEncrypted}).Append(nil), // not offered
		cat(setup, req.Append(nil)),
		cat(setup, req.Append(nil), start),
		cat(setup, req.Append(nil), start, stop),
		cat(setup, req.Append(nil), start, stop, req.Append(nil), start),
		cat(setup, huge.Append(nil), start),
		cat(setup, req.Append(nil)[:50]), // partial request
		cat(setup, []byte{0xff}, make([]byte, 15)),
		cat(setup, bytes.Repeat(req.Append(nil), 10)),                      // past the session limits
		cat(setup, start, start, stop, (&wire.StopSessions{}).Append(nil)), // nothing to start
		authSeed(wire.SecurityAuthenticated),
		authSeed(wire.SecurityEncrypted),
		authSeed(wire.SecurityMixed),
		authSeed(wire.SecurityEncrypted)[:wire.SetUpResponseSize+40], // cut inside the encrypted request
		// Individual Session Control and RFC 6038.
		cat(isc, req.Append(nil), startN(wire.SID{1}), stopN(wire.SID{1}), req.Append(nil)),
		cat(isc, startN(), stopN(make([]wire.SID, 40)...)),
		cat(isc, start, stop),
		cat((&wire.SetUpResponse{Mode: wire.ModeUnauthenticated | wire.ModeReflectOctets | wire.ModeSymmetricalSize}).Append(nil), reflect.Append(nil), start),
	}
}

// FuzzServerConn feeds arbitrary octets to one control connection and checks
// that the server neither panics nor hangs, ends the connection within its
// timers, and leaks no session, socket or goroutine: synctest.Test fails if
// any goroutine of the bubble is left behind.
func FuzzServerConn(f *testing.F) {
	for _, s := range abuseSeeds() {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, input []byte, hangUp bool) {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, abuseConfig())
			conn, done := hs.dial(clientAddr)
			go func() { _, _ = io.Copy(io.Discard, conn) }() // read whatever the server says
			go func() {
				_, _ = conn.Write(input)
				if hangUp {
					_ = conn.Close()
				}
			}()
			// Without hanging up, the connection ends at the latest when
			// SERVWAIT, REFWAIT or the partial message timeout fires.
			deadline := time.After(abuseServWait + abuseRefWait + abusePartial + time.Second)
			select {
			case <-done:
			case <-deadline:
				t.Fatalf("connection still served after every timer expired (input %x)", input)
			}
			_ = conn.Close()
			// Draining sessions are released at the latest REFWAIT later.
			time.Sleep(abuseRefWait + time.Second)
			synctest.Wait()
			st := hs.s.Status()
			if st.ConnectionsActive != 0 || st.SessionsActive != 0 {
				t.Fatalf("leak after the connection ended: %+v (input %x)", st, input)
			}
			hs.h.mu.Lock()
			defer hs.h.mu.Unlock()
			for i, s := range hs.h.sessions {
				if _, _, closed := s.state(); closed != 1 {
					t.Fatalf("session %d closed %d times (input %x)", i, closed, input)
				}
			}
		})
	})
}

// TestLimitSlotsComeBack checks that connections and sessions refused at the
// limit are accepted again once earlier ones are gone.
func TestLimitSlotsComeBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{MaxConnections: 2, MaxSessions: 3, MaxSessionsPerConnection: 2})
		c1, _, done1 := hs.handshake()
		c2, _, _ := hs.handshake()

		// Connection limit + 1 is refused with Modes 0.
		conn, refused := hs.dial(clientAddr)
		if _, err := client.Handshake(t.Context(), conn, client.Config{}); err == nil {
			t.Fatal("third connection accepted over MaxConnections 2")
		}
		<-refused

		// Sessions: 2 on the first connection, 1 on the second, then the
		// server-wide limit refuses with Accept 5.
		for _, c := range []*client.Client{c1, c1, c2} {
			if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c2.Request(t.Context(), client.Request{SenderPort: 50000}); err == nil {
			t.Fatal("fourth session accepted over MaxSessions 3")
		} // the client closes the connection on a rejection

		// Ending the first connection releases its accepted sessions and its
		// connection slot.
		_ = c1.Close()
		<-done1
		synctest.Wait()
		c3, _, _ := hs.handshake()
		for range 2 {
			if _, err := c3.Request(t.Context(), client.Request{SenderPort: 50000}); err != nil {
				t.Fatalf("session refused after slots were freed: %v", err)
			}
		}
		if st := hs.s.Status(); st.SessionsActive != 2 || st.ConnectionsActive != 1 {
			t.Errorf("status = %+v", st)
		}
		_ = c3.Close()
	})
}

// TestSlowloris checks that idle and half-sent connections, up to the
// connection limit, do not stop a normal client within the limit, and that
// SERVWAIT and the partial message timeout reclaim them.
func TestSlowloris(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, abuseConfig()) // MaxConnections 4
		var idle []net.Conn
		var ended []<-chan struct{}
		// Two connections read the greeting and send nothing; one sends half
		// a Set-Up-Response.
		for i := range 3 {
			conn, done := hs.dial(clientAddr)
			raw{t, conn}.recv(wire.ServerGreetingSize)
			if i == 2 {
				raw{t, conn}.send(make([]byte, 20))
			}
			idle = append(idle, conn)
			ended = append(ended, done)
		}
		// A normal client still gets through and runs a session.
		c, _, done := hs.handshake()
		if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); err != nil {
			t.Fatalf("normal client blocked by idle connections: %v", err)
		}
		if err := c.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		<-done

		// The half-sent one is dropped after the partial message timeout, the
		// idle ones after SERVWAIT.
		<-ended[2]
		for _, e := range ended[:2] {
			<-e
		}
		if st := hs.s.Status(); st.ConnectionsActive != 0 {
			t.Errorf("idle connections left: %+v", st)
		}
		for _, conn := range idle {
			_ = conn.Close()
		}
	})
}

// TestSIDCollision checks that a random source that keeps repeating cannot
// give two sessions the same SID: the server answers Accept 2 instead.
func TestSIDCollision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, server.Config{Rand: constRand{}})
		r, _ := hs.rawHandshake()
		first := r.request(validRequest())
		if first.Accept != wire.AcceptOK {
			t.Fatalf("first request: %+v", first)
		}
		// Same address, same synthetic time, same "random" octets.
		second := r.request(validRequest())
		if second.Accept != wire.AcceptInternalError || second.SID == first.SID {
			t.Errorf("second request: %+v, want Accept 2 and no duplicate SID", second)
		}
		if n := hs.s.Status().SessionsActive; n != 1 {
			t.Errorf("%d sessions, want 1", n)
		}
	})
}

// constRand returns the same octets for every read.
type constRand struct{}

func (constRand) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 7
	}
	return len(b), nil
}

// TestAuthSeeds checks that the authenticated fuzz seeds are valid
// exchanges, so that the fuzzer starts from inside the protected streams.
func TestAuthSeeds(t *testing.T) {
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted, wire.SecurityMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, abuseConfig())
				conn, done := hs.dial(clientAddr)
				go func() { _, _ = io.Copy(io.Discard, conn) }()
				if _, err := conn.Write(authSeed(mode)); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if len(hs.h.tried) != 1 {
					t.Fatalf("sessions opened: %v", hs.h.tried)
				}
				if s := hs.h.session(0); s.spec.Security != mode.TestMode() {
					t.Errorf("spec %+v", s.spec)
				}
				if st := hs.s.Sessions(); len(st) != 1 || st[0].State != server.SessionDraining {
					t.Errorf("sessions %+v", st)
				}
				_ = conn.Close()
				<-done
			})
		})
	}
}
