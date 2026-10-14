//declscope:namespace light

package reflector_test

import (
	"bytes"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Akkanbe/gotwamp/internal/reflector"
	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// abuseMaxPacket is the max_packet_size of the reflector fuzz targets.
//
//declscope:package // shared by the Light and Full fuzz targets
const abuseMaxPacket = 2000

// checkReply checks a reflector's answer to payload against the invariants
// of SPEC §6.4 and §6.6: nothing for a packet outside 14..max_packet_size,
// otherwise one reply of exactly max(n, header) octets, so never more than
// that (the amplification bound), echoing the sender's fields.
//
//declscope:package // the Full reflector fuzz checks its replies the same way
func checkReply(t *testing.T, payload []byte, out []txPacket, header int) {
	t.Helper()
	n := min(len(payload), abuseMaxPacket+1) // the socket truncates to the buffer
	if n < wire.SenderHeaderSize || n > abuseMaxPacket {
		if len(out) != 0 {
			t.Fatalf("%d-octet packet reflected", n)
		}
		return
	}
	if len(out) != 1 {
		t.Fatalf("%d replies to a valid %d-octet packet", len(out), n)
	}
	r := out[0].b
	if len(r) != max(n, header) {
		t.Fatalf("reply of %d octets to %d, want %d", len(r), n, max(n, header))
	}
	if !bytes.Equal(r[24:28], payload[0:4]) || !bytes.Equal(r[28:36], payload[4:12]) || !bytes.Equal(r[36:38], payload[12:14]) {
		t.Fatalf("sender fields not echoed: request %x reply %x", payload[:14], r[24:38])
	}
	if pad := r[header:]; !bytes.Equal(pad, payload[wire.SenderHeaderSize:wire.SenderHeaderSize+len(pad)]) {
		t.Fatal("reply padding is not the request's, truncated at the end")
	}
}

func FuzzLightReply(f *testing.F) {
	for _, n := range []int{0, 13, 14, 40, 41, 44, 45, 1472, abuseMaxPacket, abuseMaxPacket + 1, 9000} {
		f.Add(bytes.Repeat([]byte{0xa5}, n), int16(-1), int16(64), false)
		f.Add(bytes.Repeat([]byte{0x5a}, n), int16(0xb9), int16(-1), true)
	}
	f.Fuzz(func(t *testing.T, payload []byte, tos, ttl int16, dscpecn bool) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, reflector.Config{MaxPacketSize: abuseMaxPacket, DSCPECNMonitoring: dscpecn}, reflector.Deps{})
			out := h.send(0, payload, senderA, tstamp.RxMeta{TTL: int(ttl), TOS: int(tos)})
			header := wire.ReflectorHeaderSize
			if dscpecn {
				header = wire.ReflectorDSCPECNHeaderSize
			}
			checkReply(t, payload, out, header)
			if len(out) == 1 {
				if want := byte(ttl); (ttl < 0 || ttl > 255) && out[0].b[40] != 255 || ttl >= 0 && ttl <= 255 && out[0].b[40] != want {
					t.Fatalf("Sender TTL %d for received TTL %d", out[0].b[40], ttl)
				}
			}
		})
	})
}

// TestFlowTableFlood sends from many more sources than max_flows: memory
// stays bounded, every packet is still answered (statelessly past the
// limit), and the overflow is counted.
func TestFlowTableFlood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{MaxFlows: 100}, reflector.Deps{})
		const sources = 1000
		reflected := 0
		for i := range sources {
			from := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), 50000)
			reflected += len(h.send(0, senderPacket(uint32(i), 41), from, tstamp.RxMeta{TTL: 64, TOS: -1}))
		}
		st := h.light.Status()
		if reflected != sources || st.FlowsActive != 100 || st.FlowsOverflow != sources-100 || len(h.light.Flows()) != 100 {
			t.Errorf("reflected %d, status %+v", reflected, st)
		}
		// Expired flows make room for new ones again.
		time.Sleep(reflector.DefaultFlowTimeout + time.Minute)
		synctest.Wait()
		h.send(0, senderPacket(1, 41), netip.MustParseAddrPort("192.0.2.200:1"), tstamp.RxMeta{TTL: 64, TOS: -1})
		if st := h.light.Status(); st.FlowsActive != 1 {
			t.Errorf("flows after expiry = %d, want 1", st.FlowsActive)
		}
	})
}

// TestRateLimitSpoofedFlood sends one packet from each of many spoofed
// sources with a rate limit on: each source gets its burst, and nothing
// grows without bound (the bucket table is capped, ratelimit_test.go).
func TestRateLimitSpoofedFlood(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, reflector.Config{RateLimitPPS: 1}, reflector.Deps{})
		for i := range 500 {
			from := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 1, byte(i >> 8), byte(i)}), 1)
			h.send(0, senderPacket(1, 41), from, tstamp.RxMeta{TTL: 64, TOS: -1})
			h.send(0, senderPacket(2, 41), from, tstamp.RxMeta{TTL: 64, TOS: -1}) // over the 1 pps burst
		}
		st := h.light.Status()
		if st.Counters.Reflected != 500 || st.Counters.Dropped[reflector.DropRateLimit] != 500 {
			t.Errorf("counters %+v", st.Counters)
		}
	})
}

// FuzzLightSTAMPReply feeds arbitrary datagrams to a Light reflector in
// STAMP mode: no panic, nothing below 14 or above max_packet_size answered,
// otherwise one reply of max(n, 44) octets with the SSID copied, the Sender
// fields echoed, and the octets after the base kept but for the TLV flags.
func FuzzLightSTAMPReply(f *testing.F) {
	f.Add(stampPacket(1, 7, []byte{0x80, 1, 0, 2, 9, 9}))
	f.Add(stampPacket(1, 0, []byte{0, 1, 0xff, 0xff}))
	f.Add(bytes.Repeat([]byte{0xa5}, 14))
	f.Add(bytes.Repeat([]byte{0xa5}, abuseMaxPacket+1))
	f.Fuzz(func(t *testing.T, payload []byte) {
		synctest.Test(t, func(t *testing.T) {
			h := start(t, reflector.Config{MaxPacketSize: abuseMaxPacket, STAMP: true}, reflector.Deps{})
			out := h.send(0, payload, senderA, tstamp.RxMeta{TTL: 64, TOS: 0})
			n := len(payload)
			if n < wire.SenderHeaderSize || n > abuseMaxPacket {
				if len(out) != 0 {
					t.Fatalf("answered %d octets", n)
				}
				return
			}
			if len(out) != 1 || len(out[0].b) != max(n, wire.STAMPBaseSize) {
				t.Fatalf("%d replies to %d octets", len(out), n)
			}
			r := out[0].b
			if wire.STAMPSSID(r) != wire.STAMPSSID(payload) || !bytes.Equal(r[24:38], append(append([]byte{}, payload[0:4]...), payload[4:14]...)) {
				t.Fatalf("fields not echoed: %x for %x", r[:44], payload)
			}
			if n > wire.STAMPBaseSize {
				for i := wire.STAMPBaseSize; i < n; i++ {
					if r[i] != payload[i] && r[i]&^(wire.TLVUnrecognized|wire.TLVMalformed) != payload[i]&^(wire.TLVUnrecognized|wire.TLVMalformed) {
						t.Fatalf("octet %d changed from %#x to %#x", i, payload[i], r[i])
					}
				}
			}
		})
	})
}
