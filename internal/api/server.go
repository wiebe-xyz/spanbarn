package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	APIKey         string
	MaxBodyBytes   int64
	AllowedOrigins []string
	Version        string
}

// Server is the HTTP server for SpanBarn.
type Server struct {
	mux            *http.ServeMux
	handler        http.Handler
	apiKey         string
	maxBodyBytes   int64
	allowedOrigins []string
	version        string
	ingest         *ingest.Handler
	querySvc       *service.QueryService
	sessionMgr     *auth.SessionManager
	logger         *slog.Logger
}

// NewServer creates a new HTTP server with the given configuration.
func NewServer(cfg ServerConfig, ingestHandler *ingest.Handler, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20 // 4 MiB default
	}

	s := &Server{
		mux:            http.NewServeMux(),
		apiKey:         cfg.APIKey,
		maxBodyBytes:   cfg.MaxBodyBytes,
		allowedOrigins: cfg.AllowedOrigins,
		version:        cfg.Version,
		ingest:         ingestHandler,
		logger:         logger,
	}

	s.registerRoutes()

	// Build middleware chain: recovery -> logging -> CORS -> maxBodyBytes -> routes.
	var h http.Handler = s.mux
	h = maxBodyBytesMiddleware(s.maxBodyBytes, h)
	h = corsMiddleware(s.allowedOrigins, h)
	h = loggingMiddleware(logger, h)
	h = recoveryMiddleware(logger, h)
	s.handler = h

	return s
}

// NewServerWithQuery creates a new HTTP server with query service support.
func NewServerWithQuery(cfg ServerConfig, ingestHandler *ingest.Handler, querySvc *service.QueryService, sm *auth.SessionManager, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 4 << 20 // 4 MiB default
	}

	s := &Server{
		mux:            http.NewServeMux(),
		apiKey:         cfg.APIKey,
		maxBodyBytes:   cfg.MaxBodyBytes,
		allowedOrigins: cfg.AllowedOrigins,
		version:        cfg.Version,
		ingest:         ingestHandler,
		querySvc:       querySvc,
		sessionMgr:     sm,
		logger:         logger,
	}

	s.registerRoutes()

	// Build middleware chain: recovery -> logging -> CORS -> maxBodyBytes -> routes.
	var h http.Handler = s.mux
	h = maxBodyBytesMiddleware(s.maxBodyBytes, h)
	h = corsMiddleware(s.allowedOrigins, h)
	h = loggingMiddleware(logger, h)
	h = recoveryMiddleware(logger, h)
	s.handler = h

	return s
}

// Handler returns the fully wrapped http.Handler for use with httptest or http.Server.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// ListenAndServe starts the HTTP server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	s.logger.Info("listening", "addr", addr)
	return srv.ListenAndServe()
}

// ListenAndServeContext starts the server and shuts down gracefully on context cancellation.
func (s *Server) ListenAndServeContext(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
