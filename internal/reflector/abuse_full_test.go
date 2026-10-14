//declscope:namespace full

package reflector_test

import (
	"bytes"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/control/server"
	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

func FuzzFullReply(f *testing.F) {
	for _, n := range []int{0, 13, 14, 41, 44, 1000, abuseMaxPacket + 1} {
		f.Add(bytes.Repeat([]byte{0xa5}, n), false, false)
		f.Add(bytes.Repeat([]byte{0x5a}, n), true, true)
	}
	f.Fuzz(func(t *testing.T, payload []byte, dscpecn, wrongSource bool) {
		synctest.Test(t, func(t *testing.T) {
			h := newFull(t, reflector.FullConfig{MaxPacketSize: abuseMaxPacket})
			s, c := h.open(server.SessionSpec{DSCPECNMonitoring: dscpecn})
			s.Start(time.Now())
			from := fullSender
			if wrongSource {
				from = netip.MustParseAddrPort("203.0.113.9:50000")
			}
			out := send(c, payload, from)
			if wrongSource {
				if len(out) != 0 {
					t.Fatal("reflected a packet from another source")
				}
				return
			}
			header := wire.ReflectorHeaderSize
			if dscpecn {
				header = wire.ReflectorDSCPECNHeaderSize
			}
			checkReply(t, payload, out, header)
		})
	})
}
