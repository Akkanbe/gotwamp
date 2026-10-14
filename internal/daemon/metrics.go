package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Akkanbe/gotwamp/internal/export/prom"
)

// metricsServer serves /metrics and /healthz (SPEC §9.6).
//
//declscope:package // Start runs it
type metricsServer struct {
	ln net.Listener
	//declscope:private
	srv *http.Server
}

// listenMetrics binds the metrics endpoint of c at addr.
//
//declscope:package // Start runs it
func listenMetrics(addr string, c *prom.Collector) (*metricsServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics listen %s: %w", addr, err)
	}
	return &metricsServer{ln: ln, srv: &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}}, nil
}

// serve serves until shutdown.
//
//declscope:package // Start runs it
func (m *metricsServer) serve() error {
	if err := m.srv.Serve(m.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("metrics: %w", err)
	}
	return nil
}

// shutdown stops serving, waiting up to a second, and no longer than
// limit, for scrapes in flight.
//
//declscope:package // the daemon stops it
func (m *metricsServer) shutdown(limit time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), min(limit, time.Second))
	defer cancel()
	if err := m.srv.Shutdown(ctx); err != nil {
		_ = m.srv.Close()
	}
}

// addr returns the bound address.
//
//declscope:package // Daemon.MetricsAddr reports it
func (m *metricsServer) addr() net.Addr { return m.ln.Addr() }
