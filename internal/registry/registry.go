// Package registry serves the read-only OCI distribution API that presents
// upstream Helm repositories as OCI registries, and manages the HTTP listener
// lifecycle.
package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/home-operations/ocify/internal/config"
	"golang.org/x/sync/errgroup"
)

// Connection timeouts applied to every listener, bounding slow-client
// (Slowloris) and idle keep-alive resource exhaustion. The write timeout is
// sized for a slow client pulling a max-size chart blob, not a webhook-style
// exchange.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 120 * time.Second
	idleTimeout       = 120 * time.Second
)

// newHTTPServer builds an http.Server with ocify's standard connection
// timeouts.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// Server owns the configured listeners and the resolver.
type Server struct {
	cfg *config.Config
	res *Resolver
	log *slog.Logger
}

// New constructs a Server from the resolved config and resolver.
func New(cfg *config.Config, res *Resolver, log *slog.Logger) *Server {
	return &Server{cfg: cfg, res: res, log: log}
}

// Run starts every enabled listener and blocks until ctx is cancelled or a
// listener fails, then drains them within the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)

	registrySrv := newHTTPServer(fmt.Sprintf(":%d", s.cfg.Port), s.handler())
	g.Go(func() error { return serve(registrySrv, "registry", s.log) })

	var metricsSrv *http.Server
	if s.cfg.MetricsEnabled {
		metricsSrv = newHTTPServer(fmt.Sprintf(":%d", s.cfg.MetricsPort), s.accessLog(metricsHandler()))
		g.Go(func() error { return serve(metricsSrv, "metrics", s.log) })
	}

	g.Go(func() error {
		<-gctx.Done()
		s.log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
		defer cancel()
		shutdown(sctx, registrySrv)
		shutdown(sctx, metricsSrv)
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func serve(srv *http.Server, name string, log *slog.Logger) error {
	log.Info("listening", "server", name, "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("%s server: %w", name, err)
	}
	return nil
}

func shutdown(ctx context.Context, srv *http.Server) {
	if srv == nil {
		return
	}
	_ = srv.Shutdown(ctx)
}
