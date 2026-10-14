//declscope:namespace light

package reflector

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// nopConn is a tstamp.Conn that discards what is sent and never receives.
type nopConn struct{ local netip.AddrPort }

func (nopConn) WriteTo([]byte, netip.AddrPort, int) (time.Time, error) { return time.Time{}, nil }
func (nopConn) ReadFrom([]byte) (int, netip.AddrPort, tstamp.RxMeta, error) {
	select {}
}
func (c nopConn) LocalAddr() netip.AddrPort     { return c.local }
func (nopConn) SetReadDeadline(time.Time) error { return nil }
func (nopConn) Mode() tstamp.Mode               { return tstamp.ModeUserspace }
func (nopConn) Close() error                    { return nil }

// BenchmarkLightHandle measures the reflector's own work on one packet of
// the unauthenticated Light path, without the socket: ACL, flow table,
// Error Estimate, reply and T3.
func BenchmarkLightHandle(b *testing.B) {
	local := netip.MustParseAddrPort("127.0.0.1:862")
	l, err := ListenLight(Config{Listeners: []netip.AddrPort{local}}, Deps{
		Listen: func(string, netip.AddrPort, tstamp.Options) (tstamp.Conn, error) { return nopConn{local}, nil },
	})
	if err != nil {
		b.Fatal(err)
	}
	ln := l.listeners[0]
	est := estimator{}
	pkt := (&wire.SenderPacket{Padding: make([]byte, 27)}).Append(nil)
	buf := make([]byte, 9001)
	from := netip.MustParseAddrPort("127.0.0.2:50000")
	b.ReportAllocs()
	for b.Loop() {
		n := copy(buf, pkt)
		l.handle(ln, &est, buf, n, from, tstamp.RxMeta{At: time.Now(), TTL: 64, TOS: 0})
	}
}
