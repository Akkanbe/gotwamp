package cli

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/testpki"
)

// remoteDaemon serves the fake daemon over TCP with mutual TLS, accepting
// client certificates ca signed, and returns its address.
func remoteDaemon(t *testing.T, ca *testpki.CA) string {
	t.Helper()
	server := ca.Server("gotwampd")
	pair, err := tls.LoadX509KeyPair(server.Cert, server.Key)
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(ca.Path)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
	})))
	gotwampv1.RegisterDaemonServiceServer(srv, newFakeDaemon())
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

// runRemote runs gotwamp with args and the environment env, with no
// in-process dialer: --server is dialed for real.
func runRemote(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(t.Context(), args, BuildInfo{Version: "v1"}, Env{Stdout: &out, Stderr: &errb, Getenv: func(k string) string { return env[k] }})
	return code, out.String(), errb.String()
}

// TestRemoteServer: --server reaches gotwampd over TCP with mutual TLS, the
// files coming from flags or from the environment; TCP without TLS is
// refused before dialing (ADR 0010).
func TestRemoteServer(t *testing.T) {
	ca := testpki.NewCA(t, "test")
	addr := remoteDaemon(t, ca)
	admin := ca.Client("admin")

	code, out, errs := runRemote(t, nil, "--server", addr, "--tls-ca", ca.Path, "--tls-cert", admin.Cert, "--tls-key", admin.Key, "status")
	if code != ExitOK || !strings.Contains(out, "v0.2.0") {
		t.Errorf("status over TCP: exit %d\n%s%s", code, out, errs)
	}
	env := map[string]string{"GOTWAMP_SERVER": addr, "GOTWAMP_TLS_CA": ca.Path, "GOTWAMP_TLS_CERT": admin.Cert, "GOTWAMP_TLS_KEY": admin.Key}
	if code, out, errs := runRemote(t, env, "status"); code != ExitOK {
		t.Errorf("status from the environment: exit %d\n%s%s", code, out, errs)
	}
	// A certificate of another CA: the daemon refuses the handshake.
	rogue := testpki.NewCA(t, "rogue").Client("admin")
	if code, _, _ := runRemote(t, nil, "--server", addr, "--tls-ca", ca.Path, "--tls-cert", rogue.Cert, "--tls-key", rogue.Key, "status"); code != ExitUnavailable {
		t.Errorf("rogue client: exit %d, want %d", code, ExitUnavailable)
	}
	// A daemon whose certificate does not chain to --tls-ca.
	other := testpki.NewCA(t, "other")
	if code, _, _ := runRemote(t, nil, "--server", addr, "--tls-ca", other.Path, "--tls-cert", admin.Cert, "--tls-key", admin.Key, "status"); code != ExitUnavailable {
		t.Errorf("untrusted daemon: exit %d, want %d", code, ExitUnavailable)
	}
	for _, args := range [][]string{
		{"--server", addr, "status"},
		{"--server", addr, "--tls-ca", ca.Path, "status"},
		{"--server", "nohostport", "--tls-ca", ca.Path, "--tls-cert", admin.Cert, "--tls-key", admin.Key, "status"},
		{"--tls-ca", ca.Path, "status"},
		{"--server", addr, "--tls-ca", "/nonexistent", "--tls-cert", admin.Cert, "--tls-key", admin.Key, "status"},
	} {
		if code, _, _ := runRemote(t, nil, args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}
