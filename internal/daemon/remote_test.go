package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
	"github.com/Akkanbe/gotwamp/internal/testpki"
)

// remoteStatus calls GetStatus over TCP at addr, trusting ca and showing
// client (no certificate when its files are empty).
func remoteStatus(t *testing.T, addr, ca string, client testpki.Pair) error {
	t.Helper()
	pem, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	if client.Cert != "" {
		pair, err := tls.LoadX509KeyPair(client.Cert, client.Key)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = gotwampv1.NewDaemonServiceClient(conn).GetStatus(ctx, &gotwampv1.GetStatusRequest{})
	return err
}

// TestRemoteAPI: the API over TCP answers a client whose certificate the CA
// signed and whose name is allowed, and refuses a client without a
// certificate, with a name not allowed, or signed by another CA (ADR 0010).
func TestRemoteAPI(t *testing.T) {
	ca := testpki.NewCA(t, "test")
	server := ca.Server("gotwampd")
	c := testConfig(t)
	c.Daemon.API.TCP = config.APITCP{Listen: "127.0.0.1:0", CA: ca.Path, Cert: server.Cert, Key: server.Key, AllowedNames: []string{"admin", "ops.example"}}
	r := start(t, c, Options{})
	defer r.stop(t)
	addr := r.d.RemoteAddr().String()

	if err := remoteStatus(t, addr, ca.Path, ca.Client("admin")); err != nil {
		t.Errorf("allowed client (CN): %v", err)
	}
	if err := remoteStatus(t, addr, ca.Path, ca.Client("robot", "ops.example")); err != nil {
		t.Errorf("allowed client (DNS SAN): %v", err)
	}
	for name, client := range map[string]testpki.Pair{
		"no certificate":   {},
		"name not allowed": ca.Client("intruder"),
		"another CA":       testpki.NewCA(t, "rogue").Client("admin"),
	} {
		if err := remoteStatus(t, addr, ca.Path, client); err == nil {
			t.Errorf("%s: GetStatus succeeded", name)
		}
	}
	waitFor(t, "the refusals in the log", func() bool { return strings.Count(r.logs.String(), "API client refused") >= 3 })
	// The Unix domain socket still answers.
	if _, err := gotwampv1.NewDaemonServiceClient(r.conn).GetStatus(t.Context(), &gotwampv1.GetStatusRequest{}); err != nil {
		t.Errorf("GetStatus over the socket: %v", err)
	}
}

// TestRemoteAPIStartup: unreadable TLS material stops the start (exit 2).
func TestRemoteAPIStartup(t *testing.T) {
	ca := testpki.NewCA(t, "test")
	c := testConfig(t)
	c.Daemon.API.TCP = config.APITCP{Listen: "127.0.0.1:0", CA: ca.Path, Cert: ca.Path, Key: ca.Path}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d, err := Start(ctx, c, Options{})
	if err == nil {
		cancel()
		_ = d.Wait()
		t.Fatal("started with a CA certificate for its key")
	}
	if !errors.Is(err, ErrStartup) {
		t.Errorf("err = %v, want ErrStartup", err)
	}
}

// flakyListener fails its first Accept as a process out of descriptors does.
type flakyListener struct {
	net.Listener
	failed atomic.Bool
}

func (f *flakyListener) Accept() (net.Conn, error) {
	if f.failed.CompareAndSwap(false, true) {
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept4", syscall.EMFILE)}
	}
	return f.Listener.Accept()
}

// remoteListener returns a TLS listener of the API over TCP on loopback,
// with its log, and a client configuration it accepts.
func remoteListener(t *testing.T, wrap func(net.Listener) net.Listener) (*tlsListener, *syncBuffer, *tls.Config) {
	t.Helper()
	ca := testpki.NewCA(t, "test")
	server, client := ca.Server("gotwampd"), ca.Client("admin")
	cfg, err := remoteTLS(config.APITCP{CA: ca.Path, Cert: server.Cert, Key: server.Key})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	l := newTLSListener(wrap(ln), cfg, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { _ = l.Close() })
	pem, err := os.ReadFile(ca.Path)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	pair, err := tls.LoadX509KeyPair(client.Cert, client.Key)
	if err != nil {
		t.Fatal(err)
	}
	return l, &logs, &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: "localhost", MinVersion: tls.VersionTLS13}
}

// TestRemoteAcceptRetry: a failed Accept (EMFILE) is retried; it does not
// end the API over TCP.
func TestRemoteAcceptRetry(t *testing.T) {
	l, logs, ccfg := remoteListener(t, func(ln net.Listener) net.Listener { return &flakyListener{Listener: ln} })
	go func() {
		c, err := tls.Dial("tcp", l.Addr().String(), ccfg)
		if err == nil {
			_ = c.Handshake()
			_ = c.Close()
		}
	}()
	// Fail rather than hang when no connection comes.
	defer time.AfterFunc(10*time.Second, func() { _ = l.Close() }).Stop()
	c, err := l.Accept()
	if err != nil {
		t.Fatalf("Accept after a failed accept: %v\n%s", err, logs.String())
	}
	_ = c.Close()
	if !strings.Contains(logs.String(), "API accept failed; retrying") {
		t.Errorf("log lacks the retry:\n%s", logs.String())
	}
	_ = l.Close()
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Accept after Close = %v, want net.ErrClosed", err)
	}
}

// TestRemoteHandshakeLimit: connections beyond maxHandshakes that have not
// completed their handshake are closed at once.
func TestRemoteHandshakeLimit(t *testing.T) {
	l, logs, _ := remoteListener(t, func(ln net.Listener) net.Listener { return ln })
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for range maxHandshakes + 1 {
		c, err := net.Dial("tcp4", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	// The connections hold their slots in the order they were accepted;
	// the last finds none and is closed.
	readErr := func(c net.Conn, wait time.Duration) error {
		_ = c.SetReadDeadline(time.Now().Add(wait))
		_, err := c.Read(make([]byte, 1))
		return err
	}
	if err := readErr(conns[maxHandshakes], 5*time.Second); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("connection over the limit: read %v, want it closed", err)
	}
	if err := readErr(conns[0], 100*time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("connection within the limit: read %v, want it kept waiting for its handshake", err)
	}
	waitFor(t, "the refusal in the log", func() bool { return strings.Contains(logs.String(), "too many TLS handshakes") })
}
