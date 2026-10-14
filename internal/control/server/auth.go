//declscope:namespace server

package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// This file is the server side of the authenticated, encrypted and mixed
// modes (RFC 4656 §3.1, RFC 5618): checking the Set-Up-Response of a client
// that chose one of them.

// validateModes checks Modes and Keys.
func (c Config) validateModes() error {
	switch {
	case c.Modes&^(wire.SecurityModes|FeatureModes) != 0:
		return fmt.Errorf("modes %v: only the security modes, individual session control, reflect octets and symmetrical size can be offered", c.Modes)
	case c.Modes&wire.SecurityModes == 0:
		return fmt.Errorf("modes %v offer no security mode", c.Modes)
	case c.Modes&wire.SecurityModes&^wire.ModeUnauthenticated != 0 && c.Keys == nil:
		return fmt.Errorf("modes %v need a key store", c.Modes)
	}
	return nil
}

// Reasons a Set-Up-Response is refused with Accept 1, for the log.
var (
	errModeNotOffered = errors.New("client chose a mode not offered")
	errUnknownKeyID   = errors.New("unknown key id")
	errBadToken       = errors.New("token does not hold the challenge: wrong passphrase")
	errIVReused       = errors.New("client-iv used before")
)

// authenticate checks the Set-Up-Response of a client choosing mode, which
// is not unauthenticated, against the greeting g: the KeyID must name a
// known secret, the Token must decrypt to the Challenge under it, and the
// Client-IV must be new (RFC 4656 §3.1, §6.10). It returns the session keys.
func (s *Server) authenticate(g *wire.ServerGreeting, setup *wire.SetUpResponse) (wire.SessionKeys, error) {
	id := wire.KeyIDString(setup.KeyID)
	pass, ok := s.cfg.Keys.Passphrase(id)
	if !ok {
		return wire.SessionKeys{}, errUnknownKeyID
	}
	key, err := wire.DeriveKey(pass, g.Salt, g.Count)
	if err != nil {
		return wire.SessionKeys{}, err
	}
	challenge, keys := wire.DecryptToken(key, setup.Token)
	// In constant time: how much of the Challenge matched must not show.
	if subtle.ConstantTimeCompare(challenge[:], g.Challenge[:]) != 1 {
		return wire.SessionKeys{}, errBadToken
	}
	// A replayed Set-Up-Response already fails on the Challenge, which is
	// fresh for every connection; a reused IV is the client's mistake, which
	// would let an observer compare the two streams.
	if !s.ivs.claim(setup.ClientIV) {
		return wire.SessionKeys{}, errIVReused
	}
	return keys, nil
}

// ivHistoryLen bounds how many Client-IVs the server remembers.
const ivHistoryLen = 4096

// ivHistory remembers the latest Client-IVs, oldest first out.
type ivHistory struct {
	mu   sync.Mutex
	seen map[[wire.IVSize]byte]struct{}
	ring [][wire.IVSize]byte
	next int
}

// claim records iv and reports whether it was new.
func (h *ivHistory) claim(iv [wire.IVSize]byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, dup := h.seen[iv]; dup {
		return false
	}
	if h.seen == nil {
		h.seen = make(map[[wire.IVSize]byte]struct{})
	}
	if len(h.ring) < ivHistoryLen {
		h.ring = append(h.ring, iv)
	} else {
		delete(h.seen, h.ring[h.next])
		h.ring[h.next] = iv
		h.next = (h.next + 1) % ivHistoryLen
	}
	h.seen[iv] = struct{}{}
	return true
}
