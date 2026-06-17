package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// ServerConfig holds configuration for the HTTP server.
type ServerConfig struct {
	APIKey             string
	MaxBodyBytes       int64
	AllowedOrigins     []string
	Version            string
	Environment        string // e.g. "production", "staging", "testing"
	MetricsToken       string // Bearer token for /metrics; empty = no auth
	LoginRate          int    // per-minute rate limit for login; 0 = default (10)
	IngestRate         int    // per-minute rate limit for ingest; 0 = default (600)
	APIRate            int    // per-minute rate limit for API queries; 0 = default (120)
	SessionSecret      string
	PublicURL          string
	FunnelBarnEndpoint string
	FunnelBarnAPIKey   string
	FunnelBarnProject  string
}

// Server is the HTTP server for SpanBarn.
type Server struct {
	mux            *http.ServeMux
	handler        http.Handler
	apiKey         string
	maxBodyBytes   int64
	allowedOrigins []string
	version        string
	environment    string
	metricsToken   string
	sessionSecret  string
	publicURL      string
	funnelBarn     funnelBarnConfig
	rateLimiter    *RateLimiter
	metrics        *Metrics
	ingest         *ingest.Handler
	metricsIngest  *ingest.MetricsHandler
	logsIngest     *ingest.LogsHandler
	traceBuffer    *ingest.TraceBuffer
	querySvc       *service.QueryService
	sessionMgr     *auth.SessionManager
	authorizer     *auth.Authorizer
	repo           *repository.Repository
	dbPath         string
	spoolDir       string
	cache          *cache.Cache
	logger         *slog.Logger
	oidc           *auth.OIDCClient
	selfMetrics    *selfmetrics.Recorder
}

// SetSelfMetricsRecorder wires the self-metrics recorder so the request
// middleware records request rate and latency. Optional; nil disables it.
func (s *Server) SetSelfMetricsRecorder(rec *selfmetrics.Recorder) {
	s.selfMetrics = rec
}

// SetOIDCClient wires the optional iambarn OIDC login adapter. When set, the
// frontend's client-config reports oidc.enabled=true and the SPA redirects to
// /api/v1/oidc/login on the login screen. Local single-user login still works.
func (s *Server) SetOIDCClient(c *auth.OIDCClient) {
	s.oidc = c
}

// funnelBarnConfig captures the public bits of the FunnelBarn integration
// that the SPA needs at boot. Empty Endpoint disables analytics.
type funnelBarnConfig struct {
	Endpoint string
	APIKey   string
	Project  string
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithRepository attaches a repository for alert CRUD endpoints.
func WithRepository(repo *repository.Repository) ServerOption {
	return func(s *Server) {
		s.repo = repo
	}
}

// WithAuthorizer attaches an Authorizer for DB-backed API key validation.
func WithAuthorizer(a *auth.Authorizer) ServerOption {
	return func(s *Server) {
		s.authorizer = a
	}
}

// WithPaths sets the database and spool directory paths for stats reporting.
func WithPaths(dbPath, spoolDir string) ServerOption {
	return func(s *Server) {
		s.dbPath = dbPath
		s.spoolDir = spoolDir
	}
}

// WithCache attaches a cache used by stats endpoints for SWR caching.
func WithCache(c *cache.Cache) ServerOption {
	return func(s *Server) {
		s.cache = c
	}
}

func WithTraceBuffer(tb *ingest.TraceBuffer) ServerOption {
	return func(s *Server) {
		s.traceBuffer = tb
	}
}

// WithMetricsHandler attaches a MetricsHandler for OTLP metrics ingest.
func WithMetricsHandler(h *ingest.MetricsHandler) ServerOption {
	return func(s *Server) {
		s.metricsIngest = h
	}
}

// WithLogsHandler attaches a LogsHandler for OTLP logs ingest.
func WithLogsHandler(h *ingest.LogsHandler) ServerOption {
	return func(s *Server) {
		s.logsIngest = h
	}
}

// NewServer creates a new HTTP server with the given configuration.
func NewServer(cfg ServerConfig, ingestHandler *ingest.Handler, logger *slog.Logger) *Server {
	return NewServerWithQuery(cfg, ingestHandler, nil, nil, logger)
}

// NewServerWithQuery creates a new HTTP server with query service support.
func NewServerWithQuery(cfg ServerConfig, ingestHandler *ingest.Handler, querySvc *service.QueryService, sm *auth.SessionManager, logger *slog.Logger, opts ...ServerOption) *Server {
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
		environment:    cfg.Environment,
		metricsToken:   cfg.MetricsToken,
		sessionSecret:  cfg.SessionSecret,
		publicURL:      cfg.PublicURL,
		funnelBarn: funnelBarnConfig{
			Endpoint: cfg.FunnelBarnEndpoint,
			APIKey:   cfg.FunnelBarnAPIKey,
			Project:  cfg.FunnelBarnProject,
		},
		rateLimiter:    NewRateLimiter(defaultRate(cfg.LoginRate, 10), defaultRate(cfg.IngestRate, 600), defaultRate(cfg.APIRate, 120)),
		metrics:        NewMetrics(),
		ingest:      ingestHandler,
		querySvc:    querySvc,
		sessionMgr:     sm,
		logger:         logger,
	}

	for _, opt := range opts {
		opt(s)
	}

	s.registerRoutes()

	// Build middleware chain: recovery -> requestID -> security -> tracing -> metrics -> logging -> CORS -> maxBodyBytes -> routes.
	var h http.Handler = s.mux
	h = maxBodyBytesMiddleware(s.maxBodyBytes, h)
	h = corsMiddleware(s.allowedOrigins, h)
	h = loggingMiddleware(logger, s.selfMetrics, h)
	h = MetricsMiddleware(s.metrics)(h)
	h = observability.TracingMiddleware(h)
	h = SecurityHeaders(h)
	h = requestIDMiddleware(h)
	h = recoveryMiddleware(logger, h)
	s.handler = h

	return s
}

func defaultRate(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
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
