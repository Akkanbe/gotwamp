package gotwamp

// CredentialStore looks up the shared secret of a TWAMP identity.
// Implementations must support concurrent calls from control connections.
type CredentialStore interface {
	// Passphrase returns the secret of id and whether it is known. The caller
	// treats the returned bytes as read-only.
	Passphrase(id string) ([]byte, bool)
}

// Credentials holds shared secrets indexed by identity. Concurrent reads are
// safe; callers must not mutate the map or its secret slices concurrently
// with measurements or reflector service.
type Credentials map[string][]byte

// Passphrase returns the shared secret of id and whether it is known.
func (c Credentials) Passphrase(id string) ([]byte, bool) {
	p, ok := c[id]
	return p, ok
}
