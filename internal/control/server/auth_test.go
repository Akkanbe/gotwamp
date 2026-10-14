//declscope:namespace server

package server_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

const allModes = wire.SecurityModes

var testKeys = keystore.Map{"alice": []byte("secretpass"), "bob": []byte("other")}

func authConfig(modes wire.Modes) server.Config {
	return server.Config{Modes: modes, Keys: testKeys}
}

func aliceClient(modes wire.Modes) client.Config {
	return client.Config{Modes: modes, KeyID: "alice", Passphrase: []byte("secretpass")}
}

// tapConn records what a connection writes and can corrupt one write.
type tapConn struct {
	net.Conn
	mu      sync.Mutex
	written []byte
	writes  int
	corrupt int // the write whose first octet is flipped, counting from 1; 0 for none
}

func (c *tapConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	if c.writes == c.corrupt {
		b = bytes.Clone(b)
		b[0] ^= 1
	}
	c.written = append(c.written, b...)
	c.mu.Unlock()
	return c.Conn.Write(b)
}

func (c *tapConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.written)
}

// TestAuthModes runs a session in each protected mode: the control messages
// after Set-Up-Response are encrypted, and both ends derive the same keys
// for the test packets, except in mixed mode, whose test packets are
// unauthenticated (RFC 5618).
func TestAuthModes(t *testing.T) {
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted, wire.SecurityMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, authConfig(allModes))
				conn, done := hs.dial(clientAddr)
				tap := &tapConn{Conn: conn}
				c, err := client.Handshake(t.Context(), tap, aliceClient(mode.Mode()))
				if err != nil {
					t.Fatal(err)
				}
				if c.Security() != mode || c.ServerStartTime().IsZero() {
					t.Errorf("security %v, start time %v", c.Security(), c.ServerStartTime())
				}
				sess, err := c.Request(t.Context(), client.Request{SenderPort: 50000, PaddingLength: 64})
				if err != nil {
					t.Fatal(err)
				}
				spec := hs.h.session(0).spec
				if sess.Security != mode.TestMode() || spec.Security != mode.TestMode() || spec.PaddingLength != 64 {
					t.Errorf("test mode client %v server %v, padding %d", sess.Security, spec.Security, spec.PaddingLength)
				}
				if sess.TestKeys != spec.TestKeys || (mode == wire.SecurityMixed) != (spec.TestKeys == wire.TestKeys{}) {
					t.Errorf("test keys client %x server %x", sess.TestKeys, spec.TestKeys)
				}
				if err := c.Start(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
					t.Fatal(err)
				}
				// A second run on the same connection: the streams go on.
				if _, err := c.Request(t.Context(), client.Request{SenderPort: 50001}); err != nil {
					t.Fatal(err)
				}
				if got := hs.s.Sessions(); len(got) != 2 || got[0].Security != mode {
					t.Errorf("sessions %+v", got)
				}
				w := tap.bytes()
				var setup wire.SetUpResponse
				_ = setup.Decode(w)
				if setup.Mode != mode.Mode() || wire.KeyIDString(setup.KeyID) != "alice" || setup.Token == [64]byte{} || setup.ClientIV == [16]byte{} {
					t.Errorf("set-up-response %+v", setup)
				}
				if req := w[wire.SetUpResponseSize : wire.SetUpResponseSize+wire.RequestTWSessionSize]; bytes.Equal(req, sess.Request.Append(nil)) {
					t.Error("Request-TW-Session went in the clear")
				}
				_ = c.Close()
				<-done
			})
		})
	}
}

// TestAuthChoice checks the mode the client picks: the strongest it accepts
// among those offered, as twping does.
func TestAuthChoice(t *testing.T) {
	tests := []struct {
		name            string
		offered, wanted wire.Modes
		want            wire.SecurityMode
	}{
		{"all", allModes, allModes, wire.SecurityEncrypted},
		{"authenticated or open", wire.ModeAuthenticated | wire.ModeUnauthenticated, allModes, wire.SecurityAuthenticated},
		{"mixed over open", wire.ModeMixed | wire.ModeUnauthenticated, allModes, wire.SecurityMixed},
		{"open only client", allModes, wire.ModeUnauthenticated, wire.SecurityUnauthenticated},
		{"none in common", wire.ModeMixed | wire.ModeUnauthenticated, wire.ModeEncrypted | wire.ModeAuthenticated, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, authConfig(tt.offered))
				conn, done := hs.dial(clientAddr)
				c, err := client.Handshake(t.Context(), conn, aliceClient(tt.wanted))
				if tt.want == 0 {
					if !errors.Is(err, client.ErrNoCommonMode) {
						t.Errorf("err = %v, want ErrNoCommonMode", err)
					}
					<-done
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if c.Security() != tt.want {
					t.Errorf("chose %v, want %v", c.Security(), tt.want)
				}
				_ = c.Close()
				<-done
			})
		})
	}
}

// TestAuthRefusals checks that the server answers Server-Start with Accept 1
// to a client that does not know the secret, and opens nothing.
func TestAuthRefusals(t *testing.T) {
	tests := []struct {
		name string
		cfg  client.Config
	}{
		{"wrong passphrase", client.Config{Modes: wire.ModeEncrypted, KeyID: "alice", Passphrase: []byte("guess")}},
		{"other identity's passphrase", client.Config{Modes: wire.ModeAuthenticated, KeyID: "bob", Passphrase: []byte("secretpass")}},
		{"unknown identity", client.Config{Modes: wire.ModeMixed, KeyID: "mallory", Passphrase: []byte("secretpass")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, authConfig(allModes))
				conn, done := hs.dial(clientAddr)
				_, err := client.Handshake(t.Context(), conn, tt.cfg)
				var ce *client.Error
				if !errors.As(err, &ce) || ce.Stage != client.StageSetup || ce.Accept != wire.AcceptFailure {
					t.Errorf("err = %v, want Accept 1 at setup", err)
				}
				<-done
				if st := hs.s.Status(); st.SessionsActive != 0 || st.ConnectionsActive != 0 {
					t.Errorf("status %+v", st)
				}
			})
		})
	}
	t.Run("mode not offered", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			hs := newHarness(t, server.Config{}) // unauthenticated only
			r := raw{t, nil}
			conn, done := hs.dial(clientAddr)
			r.conn = conn
			var g wire.ServerGreeting
			_ = g.Decode(r.recv(wire.ServerGreetingSize))
			if g.Modes != wire.ModeUnauthenticated {
				t.Errorf("offered %v", g.Modes)
			}
			r.send((&wire.SetUpResponse{Mode: wire.ModeEncrypted}).Append(nil))
			var ss wire.ServerStart
			_ = ss.Decode(r.recv(wire.ServerStartSize))
			if ss.Accept != wire.AcceptFailure || ss.StartTime != 0 {
				t.Errorf("server-start %+v", ss)
			}
			<-done
		})
	})
}

// TestAuthClientIVReuse checks that a Client-IV is accepted once: a client
// whose random source repeats gets Accept 1 on its second connection.
func TestAuthClientIVReuse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, authConfig(allModes))
		cfg := aliceClient(wire.ModeEncrypted)
		cfg.Rand = constRand{}
		conn, done := hs.dial(clientAddr)
		c, err := client.Handshake(t.Context(), conn, cfg)
		if err != nil {
			t.Fatal(err)
		}
		conn2, done2 := hs.dial(clientAddr)
		_, err = client.Handshake(t.Context(), conn2, cfg)
		var ce *client.Error
		if !errors.As(err, &ce) || ce.Accept != wire.AcceptFailure {
			t.Errorf("second connection with the same Client-IV: err = %v", err)
		}
		<-done2
		_ = c.Close()
		<-done
	})
}

// TestAuthReplay checks that a recorded Set-Up-Response does not open a new
// connection: the Token holds the Challenge of the recorded one.
func TestAuthReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hs := newHarness(t, authConfig(allModes))
		conn, done := hs.dial(clientAddr)
		tap := &tapConn{Conn: conn}
		c, err := client.Handshake(t.Context(), tap, aliceClient(wire.ModeAuthenticated))
		if err != nil {
			t.Fatal(err)
		}
		recorded := tap.bytes()[:wire.SetUpResponseSize]
		_ = c.Close()
		<-done

		conn, done = hs.dial(clientAddr)
		r := raw{t, conn}
		r.recv(wire.ServerGreetingSize)
		r.send(recorded)
		var ss wire.ServerStart
		_ = ss.Decode(r.recv(wire.ServerStartSize))
		if ss.Accept != wire.AcceptFailure {
			t.Errorf("replayed set-up-response: accept %v", ss.Accept)
		}
		<-done
	})
}

// TestAuthIntegrity corrupts one octet of a protected message on the wire:
// the receiver must drop the connection without acting on it (RFC 4656
// §6.10).
func TestAuthIntegrity(t *testing.T) {
	for _, mode := range []wire.SecurityMode{wire.SecurityAuthenticated, wire.SecurityEncrypted, wire.SecurityMixed} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, authConfig(allModes))
				conn, done := hs.dial(clientAddr)
				// Write 1 is Set-Up-Response, write 2 Request-TW-Session.
				tap := &tapConn{Conn: conn, corrupt: 2}
				c, err := client.Handshake(t.Context(), tap, aliceClient(mode.Mode()))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Request(t.Context(), client.Request{SenderPort: 50000}); err == nil {
					t.Error("request accepted despite a corrupted message")
				}
				<-done
				if len(hs.h.tried) != 0 {
					t.Error("the server opened a session for a message that failed its HMAC")
				}
			})
		})
	}
}

func TestAuthConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  server.Config
		want string
	}{
		{"auth without keys", server.Config{Modes: wire.ModeEncrypted | wire.ModeUnauthenticated}, "need a key store"},
		{"unsupported feature bit", server.Config{Modes: wire.ModeUnauthenticated | wire.ModeDSCPECNMonitoring}, "only the security modes"},
		{"no security mode", server.Config{Modes: wire.ModeReflectOctets}, "no security mode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := server.New(tt.cfg, &fakeHandler{}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if c := (server.Config{}).WithDefaults(); c.Modes != wire.ModeUnauthenticated {
		t.Errorf("default modes %v", c.Modes)
	}
}

// TestAuthChallenge: a Token is accepted only when it decrypts to the whole
// Challenge of the greeting; one octet off at either end is refused.
func TestAuthChallenge(t *testing.T) {
	for name, edit := range map[string]func(*[16]byte){
		"exact":       func(*[16]byte) {},
		"first octet": func(c *[16]byte) { c[0] ^= 1 },
		"last octet":  func(c *[16]byte) { c[15] ^= 0x80 },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hs := newHarness(t, authConfig(allModes))
				conn, done := hs.dial(clientAddr)
				r := raw{t, conn}
				var g wire.ServerGreeting
				_ = g.Decode(r.recv(wire.ServerGreetingSize))
				key, err := wire.DeriveKey([]byte("secretpass"), g.Salt, g.Count)
				if err != nil {
					t.Fatal(err)
				}
				keys, err := wire.NewSessionKeys(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				challenge := g.Challenge
				edit(&challenge)
				setup := wire.SetUpResponse{Mode: wire.ModeAuthenticated, Token: wire.EncryptToken(key, challenge, keys)}
				setup.KeyID, _ = wire.KeyID("alice")
				setup.ClientIV[0] = 1
				r.send(setup.Append(nil))
				var ss wire.ServerStart
				_ = ss.Decode(r.recv(wire.ServerStartSize))
				want := wire.AcceptFailure
				if name == "exact" {
					want = wire.AcceptOK
				}
				if ss.Accept != want {
					t.Errorf("accept %v, want %v", ss.Accept, want)
				}
				_ = conn.Close()
				<-done
			})
		})
	}
}
