//declscope:namespace light

package reflector

import (
	"net/netip"

	"github.com/Akkanbe/gotwamp/internal/tstamp"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Hook takes part in the replies of the Light reflector (SPEC §13). STAMP
// (RFC 8762) goes here: reflecting the SSID of RFC 8972 at offsets 14–15,
// and answering the TLVs that follow the 44-octet base packet.
type Hook interface {
	// Reply is called for every packet that passed the ACL, the rate limit
	// and the length check, after the reply has been built in place and
	// before T3 is written. in describes the received packet. Reply may
	// change reply and grow it up to cap(reply), the receive buffer; it
	// returns the reply length. ok false drops the packet, counted as
	// malformed. A length below 14 or above cap(reply) drops it too.
	Reply(in *HookPacket, reply []byte) (n int, ok bool)
}

// HookPacket is a received packet as a Hook sees it. The reply overwrites
// the packet, so Head keeps a copy of its start.
type HookPacket struct {
	// From is the Session-Sender; To is the listener it arrived on.
	From, To netip.AddrPort
	// Len is the length of the received UDP payload.
	Len int
	// Head holds the first octets of the packet, up to the 44 of a STAMP
	// base packet: the SSID is at 14–15.
	Head []byte
	// RX is the receive metadata: T2, TTL and TOS.
	RX tstamp.RxMeta

	headBuf [wire.ReflectorDSCPECNHeaderSize]byte
}
