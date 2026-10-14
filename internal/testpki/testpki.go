// Package testpki makes the certificates of mutual TLS tests: a self-signed
// CA, a server certificate for the loopback and client certificates, all
// written as PEM files readable by their owner only.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CA is a certificate authority of a test.
type CA struct {
	// Path is the CA certificate file.
	Path string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	dir  string
	t    testing.TB
}

// Pair is a certificate and its key, as files.
type Pair struct {
	Cert, Key string
}

// NewCA makes a CA named name in a temporary directory of t.
func NewCA(t testing.TB, name string) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ca := &CA{cert: cert, key: key, dir: t.TempDir(), t: t}
	ca.Path = ca.write(name+"-ca.pem", "CERTIFICATE", der)
	return ca
}

// Server issues a server certificate for 127.0.0.1, ::1 and localhost.
func (ca *CA) Server(name string) Pair {
	return ca.issue(name, x509.ExtKeyUsageServerAuth, func(c *x509.Certificate) {
		c.DNSNames = []string{"localhost"}
		c.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	})
}

// Client issues a client certificate with Common Name cn and the DNS names
// given.
func (ca *CA) Client(cn string, dns ...string) Pair {
	return ca.issue(cn, x509.ExtKeyUsageClientAuth, func(c *x509.Certificate) { c.DNSNames = dns })
}

func (ca *CA) issue(cn string, usage x509.ExtKeyUsage, edit func(*x509.Certificate)) Pair {
	ca.t.Helper()
	key := newKey(ca.t)
	tmpl := &x509.Certificate{
		SerialNumber: serial(ca.t),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	edit(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		ca.t.Fatal(err)
	}
	k, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		ca.t.Fatal(err)
	}
	return Pair{Cert: ca.write(cn+".pem", "CERTIFICATE", der), Key: ca.write(cn+"-key.pem", "EC PRIVATE KEY", k)}
}

func (ca *CA) write(name, kind string, der []byte) string {
	ca.t.Helper()
	path := filepath.Join(ca.dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		ca.t.Fatal(err)
	}
	return path
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
