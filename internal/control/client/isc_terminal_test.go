//declscope:namespace client // reuse the scripted control peer

package client_test

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func TestStopNMonitorLifetime(t *testing.T) {
	for _, event := range []string{"all_stopped", "one_still_active", "one_stop_refused"} {
		t.Run(event, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cc, sc := pipe(clientAddr, serverAddr)
				closePeer := make(chan struct{})
				finishPeer := sync.OnceFunc(func() { close(closePeer) })
				wait := serve(t, sc, func(s script) {
					defer sc.Close()
					iscScript(s, func(s script) {
						recvN(s, 2)
						s.send(&wire.SessionControl{Command: wire.CommandStartNAck, SIDs: []wire.SID{{1}, {2}}})
						n := 2
						if event == "one_still_active" {
							n = 1
						}
						if m := recvN(s, n); m.Command != wire.CommandStopNSessions {
							t.Errorf("stop request=%+v", m)
						}
						s.send(&wire.SessionControl{Command: wire.CommandStopNAck, SIDs: []wire.SID{{1}}})
						if n == 2 {
							accept := wire.AcceptOK
							if event == "one_stop_refused" {
								accept = wire.AcceptTemporaryLimit
							}
							s.send(&wire.SessionControl{Command: wire.CommandStopNAck, Accept: accept, SIDs: []wire.SID{{2}}})
						}
						<-closePeer
					})
				})
				defer func() { finishPeer(); _ = cc.Close(); _ = sc.Close(); wait() }()
				c, err := client.Handshake(t.Context(), cc, client.Config{Features: wire.ModeIndividualSessionControl})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				var sessions []*client.Session
				for _, port := range []uint16{1, 2} {
					s, err := c.Request(t.Context(), client.Request{SenderPort: port})
					if err != nil {
						t.Fatal(err)
					}
					sessions = append(sessions, s)
				}
				if _, err := c.StartN(t.Context(), sessions); err != nil {
					t.Fatal(err)
				}
				stopping := sessions
				if event == "one_still_active" {
					stopping = sessions[:1]
				}
				_, err = c.StopN(t.Context(), stopping)
				var want error
				if event == "one_stop_refused" {
					want = client.ErrRejected
				}
				if !errors.Is(err, want) {
					t.Errorf("StopN error=%v, want %v", err, want)
				}
				finishPeer()
				wait()
				synctest.Wait()
				if event == "all_stopped" {
					if c.State() != client.StateIdle || len(c.Started()) != 0 || c.Err() != nil {
						t.Errorf("idle client after peer close: state=%v active=%d error=%v", c.State(), len(c.Started()), c.Err())
					}
				} else if c.State() != client.StateActive || len(c.Started()) != 1 || !errors.Is(c.Err(), client.ErrControlLost) {
					t.Errorf("active client after peer close: state=%v active=%d error=%v", c.State(), len(c.Started()), c.Err())
				}
			})
		})
	}
}
