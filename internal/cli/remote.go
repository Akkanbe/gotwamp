package cli

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// remoteFlags select gotwampd's API over TCP with mutual TLS instead of the
// Unix domain socket (ADR 0010).
//
//declscope:package // the global flags hold them
type remoteFlags struct {
	server, ca, cert, key, serverName string
}

// register adds the flags to f, with defaults from the environment.
//
//declscope:package // the root command registers them
func (r *remoteFlags) register(f *pflag.FlagSet, getenv func(string) string) {
	f.StringVar(&r.server, "server", getenv("GOTWAMP_SERVER"),
		"gotwampd API over TCP, host:port, with mutual TLS instead of the socket (env GOTWAMP_SERVER)")
	f.StringVar(&r.ca, "tls-ca", getenv("GOTWAMP_TLS_CA"), "CA certificates (PEM) gotwampd's certificate must chain to (env GOTWAMP_TLS_CA)")
	f.StringVar(&r.cert, "tls-cert", getenv("GOTWAMP_TLS_CERT"), "client certificate (PEM) for --server (env GOTWAMP_TLS_CERT)")
	f.StringVar(&r.key, "tls-key", getenv("GOTWAMP_TLS_KEY"), "client private key (PEM) for --server (env GOTWAMP_TLS_KEY)")
	f.StringVar(&r.serverName, "tls-server-name", "", "name to verify gotwampd's certificate against (default: the --server host)")
}

// check refuses an incomplete remote setting: TCP goes with mutual TLS only.
//
//declscope:package // the root command checks the flags
func (r *remoteFlags) check(changed func(string) bool) error {
	if r.server == "" {
		for _, name := range []string{"tls-ca", "tls-cert", "tls-key", "tls-server-name"} {
			if changed(name) {
				return exitf(ExitUsage, "--%s applies to --server only", name)
			}
		}
		return nil
	}
	if _, _, err := net.SplitHostPort(r.server); err != nil {
		return exitf(ExitUsage, "--server %q is not host:port", r.server)
	}
	if r.ca == "" || r.cert == "" || r.key == "" {
		return exitf(ExitUsage, "--server needs --tls-ca, --tls-cert and --tls-key: gotwampd serves TCP with mutual TLS only")
	}
	return nil
}

// dial connects to gotwampd at --server. The connection is lazy, as over
// the socket: an unreachable or refusing daemon shows in the first RPC.
//
//declscope:package // conn dials the remote API through it
func (r *remoteFlags) dial() (*grpc.ClientConn, error) {
	pem, err := os.ReadFile(r.ca)
	if err != nil {
		return nil, exitf(ExitUsage, "--tls-ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, exitf(ExitUsage, "--tls-ca: no PEM certificate in %s", r.ca)
	}
	pair, err := tls.LoadX509KeyPair(r.cert, r.key)
	if err != nil {
		return nil, exitf(ExitUsage, "--tls-cert / --tls-key: %w", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: r.serverName}
	c, err := grpc.NewClient(r.server, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		return nil, exitf(ExitUnavailable, "cannot connect to gotwampd at %s: %w", r.server, err)
	}
	return c, nil
}
