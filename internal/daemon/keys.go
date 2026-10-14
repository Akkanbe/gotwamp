package daemon

import (
	"fmt"

	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/control/keystore"
	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// keyStores are the shared secrets of the authenticated, encrypted and
// mixed modes (perfSONAR pfs files), read once at startup: a changed file
// takes a restart, as sender.keys_file and reflector.control.keys_file are
// listed in config.Diff's Restart.
//
//declscope:package // Start reads them for the sender service and the control server
type keyStores struct {
	// sender is sender.keys_file; nil without one.
	sender keystore.Store
	// reflector is reflector.control.keys_file; nil without one.
	reflector keystore.Store
}

// loadKeys reads the keys files of cfg and checks that sender.defaults
// names a key that exists. A failure is a configuration error; it never
// shows a passphrase.
//
//declscope:package // Start reads the keys with it
func loadKeys(cfg *config.Config) (keyStores, error) {
	var ks keyStores
	if path := cfg.Sender.KeysFile; path != "" {
		m, err := keystore.LoadPFS(path)
		if err != nil {
			return ks, fmt.Errorf("sender.keys_file: %w", err)
		}
		ks.sender = m
	}
	if path := cfg.Reflector.Control.KeysFile; path != "" && cfg.Reflector.Enabled {
		m, err := keystore.LoadPFS(path)
		if err != nil {
			return ks, fmt.Errorf("reflector.control.keys_file: %w", err)
		}
		ks.reflector = m
	}
	if d := cfg.Sender.Defaults; d.Mode == "full" {
		if _, err := testSecurity(d, ks.sender); err != nil {
			return ks, fmt.Errorf("sender.defaults: %w", err)
		}
	}
	return ks, nil
}

// testSecret is the TWAMP-Control security of a Full test: the mode to ask
// for and, but for unauthenticated, the shared secret.
//
//declscope:package // newPlan puts it into the Full configuration
type testSecret struct {
	modes      wire.Modes
	keyID      string
	passphrase []byte
}

// testSecurity resolves the security of a Full test against the sender's
// keys.
//
//declscope:package // newPlan resolves every Full test's security with it
func testSecurity(p config.TestParams, keys keystore.Store) (testSecret, error) {
	var s testSecret
	switch p.Security {
	case "", "unauthenticated":
		s.modes = wire.ModeUnauthenticated
		return s, nil
	case "authenticated":
		s.modes = wire.ModeAuthenticated
	case "encrypted":
		s.modes = wire.ModeEncrypted
	case "mixed":
		s.modes = wire.ModeMixed
	default:
		return s, fmt.Errorf("security %q is not unauthenticated, authenticated, encrypted or mixed", p.Security)
	}
	if keys == nil {
		return s, fmt.Errorf("security %s needs sender.keys_file", p.Security)
	}
	pass, ok := keys.Passphrase(p.KeyID)
	if !ok {
		return s, fmt.Errorf("key_id %q is not in sender.keys_file", p.KeyID)
	}
	s.keyID, s.passphrase = p.KeyID, pass
	return s, nil
}

// offeredModes converts reflector.control.modes.
//
//declscope:package // the control server offers them
func offeredModes(c config.ControlServer) wire.Modes {
	var m wire.Modes
	for _, name := range c.OfferedModes() {
		switch name {
		case "unauthenticated":
			m |= wire.ModeUnauthenticated
		case "authenticated":
			m |= wire.ModeAuthenticated
		case "encrypted":
			m |= wire.ModeEncrypted
		case "mixed":
			m |= wire.ModeMixed
		}
	}
	return m
}
