//go:build linux

package reflector_test

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/client"
	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/twamp/errest"
	"github.com/Akkanbe/gotwamp/internal/twamp/ntptime"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// TestFullLoopback runs TWAMP Full end to end on loopback: the control
// client and server over TCP, the Full reflector on a real UDP socket.
func TestFullLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	full, err := reflector.NewFull(reflector.FullConfig{}, reflector.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{TestPorts: server.PortRange{Low: 41000, High: 41999}}, full, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error)
	go func() { served <- srv.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	senderPort := sender.LocalAddr().(*net.UDPAddr).AddrPort().Port()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.Handshake(t.Context(), conn, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The requested port is the sender's own, which is taken on this host,
	// so the server offers an alternate one.
	sess, err := c.Request(t.Context(), client.Request{SenderPort: senderPort, PaddingLength: 27, DSCP: 10, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !sess.AlternatePort || sess.Reflector.Port() < 41000 || sess.Reflector.Port() > 41999 {
		t.Errorf("session %+v", sess)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 2048)
	for seq := range uint32(5) {
		p := wire.SenderPacket{Seq: seq, Timestamp: ntptime.FromTime(time.Now()), ErrorEstimate: errest.Compute(time.Millisecond, false), Padding: make([]byte, 27)}
		if _, err := sender.WriteToUDPAddrPort(p.Append(nil), sess.Reflector); err != nil {
			t.Fatal(err)
		}
		_ = sender.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := sender.ReadFromUDPAddrPort(buf)
		if err != nil {
			t.Fatal(err)
		}
		var r wire.ReflectorPacket
		if err := r.Decode(buf[:n]); err != nil {
			t.Fatal(err)
		}
		if from != sess.Reflector || n != 41 || r.Seq != seq || r.SenderSeq != seq || r.SenderTTL != 64 {
			t.Errorf("reply from %v, %d octets: %+v", from, n, r)
		}
	}
	// The reflector counts a reply after sendmsg returns, which may be just
	// after the sender has already read it.
	eventually(t, func() bool {
		infos := srv.Sessions()
		return len(infos) == 1 && infos[0].Stats.Reflected == 5
	})
	infos := srv.Sessions()
	if len(infos) != 1 || infos[0].State != server.SessionActive || infos[0].Stats.Reflected != 5 ||
		infos[0].Sender != netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), senderPort) {
		t.Errorf("sessions = %+v", infos)
	}
	if err := c.Stop(t.Context(), wire.AcceptOK); err != nil {
		t.Fatal(err)
	}
	// The session drains for its 200 ms Timeout, then the server releases it.
	deadline := time.Now().Add(5 * time.Second)
	for len(srv.Sessions()) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(srv.Sessions()); n != 0 {
		t.Errorf("%d sessions left after Timeout", n)
	}
	if cnt := full.Counters(); cnt.Reflected != 5 {
		t.Errorf("counters = %+v", cnt)
	}
}
