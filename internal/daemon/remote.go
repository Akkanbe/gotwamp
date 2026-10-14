package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Akkanbe/gotwamp/internal/config"
)

// handshakeTimeout bounds the TLS handshake of an API client over TCP, so
// that a connection that never completes it holds nothing for long.
const handshakeTimeout = 10 * time.Second

// maxHandshakes bounds the TLS handshakes in progress. A connection beyond
// it is closed at once, so that a flood of connections that never complete
// their handshake holds at most this many goroutines and descriptors.
const maxHandshakes = 64

// maxAcceptDelay is the longest pause before Accept is retried after a
// failure such as running out of descriptors (EMFILE).
const maxAcceptDelay = time.Second

// listenRemote binds the API over TCP (daemon.api.tcp): mutual TLS 1.3,
// a client certificate signed by the configured CA and, when listed, one
// of the allowed names (ADR 0010).
//
//declscope:package // Start serves the API on it
func listenRemote(t config.APITCP, log *slog.Logger) (net.Listener, error) {
	cfg, err := remoteTLS(t)
	if err != nil {
		return nil, err
	}
	ap, err := config.ParseAddrPort(t.Listen)
	if err != nil {
		return nil, err
	}
	network := "tcp6"
	if ap.Addr().Is4() {
		network = "tcp4"
	}
	ln, err := net.Listen(network, ap.String())
	if err != nil {
		return nil, fmt.Errorf("api tcp listen %v: %w", ap, err)
	}
	return newTLSListener(ln, cfg, log), nil
}

// remoteTLS builds the server side of the API's mutual TLS.
func remoteTLS(t config.APITCP) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(t.Cert, t.Key)
	if err != nil {
		return nil, fmt.Errorf("daemon.api.tcp: %w", err)
	}
	pem, err := os.ReadFile(t.CA)
	if err != nil {
		return nil, fmt.Errorf("daemon.api.tcp.ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("daemon.api.tcp.ca: no PEM certificate in %s", t.CA)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		NextProtos:   []string{"h2"}, // gRPC
	}
	if names := t.AllowedNames; len(names) > 0 {
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no client certificate")
			}
			leaf := cs.PeerCertificates[0]
			if !allowedClient(leaf, names) {
				return fmt.Errorf("client %q is not in daemon.api.tcp.allowed_names", clientName(leaf))
			}
			return nil
		}
	}
	return cfg, nil
}

// allowedClient reports whether the Common Name or a Subject Alternative
// Name of c is one of names. DNS names compare without case.
func allowedClient(c *x509.Certificate, names []string) bool {
	if c.Subject.CommonName != "" && slices.Contains(names, c.Subject.CommonName) {
		return true
	}
	for _, d := range c.DNSNames {
		if slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, d) }) {
			return true
		}
	}
	ids := slices.Clone(c.EmailAddresses)
	for _, ip := range c.IPAddresses {
		ids = append(ids, ip.String())
	}
	for _, u := range c.URIs {
		ids = append(ids, u.String())
	}
	return slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(names, id) })
}

// clientName names a client certificate in the logs.
func clientName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	return c.Subject.String()
}

// tlsListener hands gRPC the connections whose TLS handshake succeeded.
// Handshakes run apart from Accept, each within handshakeTimeout, so that
// a slow or hostile client delays no one else; refusals are logged.
type tlsListener struct {
	net.Listener
	cfg   *tls.Config
	log   *slog.Logger
	conns chan net.Conn
	// slots holds a token for each handshake in progress.
	slots chan struct{}
	// failed is closed with err when the underlying Accept fails.
	failed chan struct{}
	err    error
	done   chan struct{}
	once   sync.Once
}

func newTLSListener(ln net.Listener, cfg *tls.Config, log *slog.Logger) *tlsListener {
	l := &tlsListener{
		Listener: ln, cfg: cfg, log: log,
		conns: make(chan net.Conn), slots: make(chan struct{}, maxHandshakes),
		failed: make(chan struct{}), done: make(chan struct{}),
	}
	go l.acceptLoop()
	return l
}

// acceptLoop accepts connections and starts their handshakes until the
// listener is closed. Other Accept failures, such as running out of
// descriptors, are retried after a pause, as net/http does, rather than
// end the API over TCP for good.
func (l *tlsListener) acceptLoop() {
	var (
		delay   time.Duration
		refused int       // connections over maxHandshakes not yet logged
		logged  time.Time // when such refusals were last logged
	)
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				l.err = err
				close(l.failed)
				return
			}
			delay = min(max(2*delay, 5*time.Millisecond), maxAcceptDelay)
			l.log.Warn("API accept failed; retrying", "error", err, "retry_in", delay)
			select {
			case <-time.After(delay):
				continue
			case <-l.done:
				return
			}
		}
		delay = 0
		select {
		case l.slots <- struct{}{}:
			go l.handshake(c)
		default:
			_ = c.Close()
			// One line a second at most: a flood would fill the log.
			if refused++; time.Since(logged) >= time.Second {
				l.log.Warn("API clients refused: too many TLS handshakes in progress", "refused", refused, "limit", maxHandshakes)
				refused, logged = 0, time.Now()
			}
		}
	}
}

// handshake completes the TLS handshake of c and hands it to Accept. It
// holds one of slots until the handshake ends.
func (l *tlsListener) handshake(c net.Conn) {
	tc := tls.Server(c, l.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err := tc.HandshakeContext(ctx)
	cancel()
	<-l.slots
	if err != nil {
		l.log.Warn("API client refused", "remote", c.RemoteAddr().String(), "error", err)
		_ = c.Close()
		return
	}
	l.log.Info("API client connected", "remote", c.RemoteAddr().String(),
		"client", clientName(tc.ConnectionState().PeerCertificates[0]))
	select {
	case l.conns <- tc:
	case <-l.done:
		_ = tc.Close()
	}
}

// Accept implements net.Listener.
func (l *tlsListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	case <-l.failed:
		return nil, l.err
	}
}

// Close implements net.Listener.
func (l *tlsListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}
