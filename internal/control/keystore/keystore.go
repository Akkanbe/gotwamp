// Package keystore holds the shared secrets of the authenticated, encrypted
// and mixed TWAMP modes: the passphrase of each identity, which the
// Control-Client names in the KeyID of Set-Up-Response (RFC 4656 §3.1).
//
// It reads perfSONAR's pfs files (twamp-server.pfs, twping -k), so that a
// gotwampd and a perfSONAR node can share one file: one identity and its
// hexadecimal passphrase per line, "#" comments and blank lines ignored.
//
//	# identity  passphrase in hex
//	alice       73656372657470617373
package keystore

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Akkanbe/gotwamp/internal/twamp/wire"
)

// Store looks up the passphrase of an identity.
type Store interface {
	// Passphrase returns the shared secret of id, and false for an unknown
	// identity.
	Passphrase(id string) ([]byte, bool)
}

// Map is a Store held in memory.
type Map map[string][]byte

// Passphrase returns the shared secret of id.
func (m Map) Passphrase(id string) ([]byte, bool) {
	p, ok := m[id]
	return p, ok
}

// LoadPFS reads a pfs file.
func LoadPFS(path string) (Map, error) {
	f, err := os.Open(path) //nolint:gosec // the path comes from the operator's configuration
	if err != nil {
		return nil, fmt.Errorf("keystore: %w", err)
	}
	defer f.Close()
	m, err := parsePFS(f)
	if err != nil {
		return nil, fmt.Errorf("keystore: %s: %w", path, err)
	}
	return m, nil
}

// parsePFS reads the pfs format. Every problem is reported with its line;
// a passphrase never appears in an error.
func parsePFS(r io.Reader) (Map, error) {
	m := Map{}
	var errs []error
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			errs = append(errs, fmt.Errorf("line %d: want an identity and a hexadecimal passphrase", line))
			continue
		}
		id := fields[0]
		if _, err := wire.KeyID(id); err != nil {
			errs = append(errs, fmt.Errorf("line %d: identity: %w", line, err))
			continue
		}
		pass, err := hex.DecodeString(fields[1])
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("line %d: passphrase of %q is not hexadecimal", line, id))
			continue
		case len(pass) == 0:
			errs = append(errs, fmt.Errorf("line %d: passphrase of %q is empty", line, id))
			continue
		}
		if _, dup := m[id]; dup {
			errs = append(errs, fmt.Errorf("line %d: identity %q appears twice", line, id))
			continue
		}
		m[id] = pass
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}
