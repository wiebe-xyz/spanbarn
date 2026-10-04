package api

import (
	"log/slog"
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// routeEnv carries the middleware shared by the route groups.
type routeEnv struct {
	rl          *RateLimiter
	ingestRL    func(http.Handler) http.Handler
	apiRL       func(http.Handler) http.Handler
	sessionAuth func(http.Handler) http.Handler
	readAuth    func(http.Handler) http.Handler
	ingestAuth  func(http.Handler) http.Handler
	otlpAuth    func(http.Handler) http.Handler
}

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	rl := s.rateLimiter
	env := &routeEnv{
		rl:          rl,
		ingestRL:    RateLimitMiddleware(rl, "ingest"),
		apiRL:       RateLimitMiddleware(rl, "api"),
		sessionAuth: SessionMiddleware(s.sessions),
		readAuth:    SessionOrReadKey(s.sessions, s.authorizer, s.oidcClient),
	}
	env.ingestAuth, env.otlpAuth = s.ingestAuthMiddleware()

	s.registerPublicRoutes(env)
	s.registerIngestRoutes(env)
	s.registerQueryRoutes(env)
	s.registerManagementRoutes(env)
	s.registerProjectRoutes(env)
}

// ingestAuthMiddleware returns the auth wrappers for the span ingest endpoints
// and for the OTLP endpoints.
func (s *Server) ingestAuthMiddleware() (ingestAuth, otlpAuth func(http.Handler) http.Handler) {
	if s.authorizer != nil {
		ingestAuth = func(next http.Handler) http.Handler { return authorizerOrBearerAuth(s.authorizer, next) }
		return ingestAuth, ingestAuth
	}
	ingestAuth = func(next http.Handler) http.Handler { return apiKeyAuth(s.apiKey, next) }
	otlpAuth = func(next http.Handler) http.Handler { return apiKeyOrBearerAuth(s.apiKey, next) }
	return ingestAuth, otlpAuth
}

// registerPublicRoutes mounts the health, config, identity, OIDC and metrics
// endpoints.
func (s *Server) registerPublicRoutes(env *routeEnv) {
	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

	// Client-config endpoint — public, no auth. Exposes only non-secret values
	// (e.g. funnelbarn project + ingest API key) that the SPA needs at boot.
	s.mux.HandleFunc("/api/v1/client-config", s.handleClientConfig)

	// Me endpoint — session auth required. Returns the current user's display
	// name so the SPA can show it in the profile chip without a cross-origin
	// request to IamBarn.
	if s.sessions != nil {
		s.mux.Handle("/api/v1/me", env.apiRL(env.sessionAuth(http.HandlerFunc(s.handleMe))))
	}

	// IamBarn proxy — session auth required when OIDC is configured. Forwards
	// iambarn-profile widget requests to IamBarn with Bearer auth so the
	// widget works same-origin (no cross-site cookie issues).
	// Must be under /api/ so Caddy routes it to the Go service.
	// Registered unconditionally — handleIAMProxy returns 404 when OIDC is
	// not configured. (SetOIDCClient runs after registerRoutes, so we cannot
	// gate registration on s.oidc != nil here.)
	if s.sessions != nil {
		s.mux.Handle("/api/iam-proxy/", env.sessionAuth(http.HandlerFunc(s.handleIAMProxy)))
	}

	// IAMBarn theme manifest — public, no auth, no redirects. Served at the
	// well-known path so IAMBarn can adopt SpanBarn's brand on its login page
	// when users arrive via an OAuth authorize redirect from this host.
	s.mux.HandleFunc("/.well-known/iambarn-theme.json", s.handleThemeManifest)

	s.registerOIDCRoutes(env)

	// Metrics endpoint.
	if s.metrics != nil {
		s.mux.Handle("/metrics", s.metrics.Handler(s.metricsToken))
	}

	// Browser crash reports — rate limited but NOT session-authed: a crash on
	// the login page happens before any session exists, and a 401 there would
	// drop exactly the errors that stop operators from logging in. The handler
	// truncates every field, and the global body cap bounds the request.
	s.mux.Handle("/api/v1/client-errors", env.ingestRL(http.HandlerFunc(s.handleClientError)))

	// Setup endpoint — intentionally public (onboarding), but rate-limited and
	// GET-only + read-idempotent (see handleSetup) so anonymous callers cannot
	// use it to spam projects or amplify writes.
	if s.repo != nil {
		s.mux.Handle("/api/v1/setup/{slug}", env.apiRL(http.HandlerFunc(s.handleSetup)))
	}
}

// registerOIDCRoutes mounts the OIDC login flow and session refresh.
func (s *Server) registerOIDCRoutes(env *routeEnv) {
	// OIDC login flow — public, no auth required. Returns 404 when OIDC is not
	// configured server-side, so the SPA can fall through to local login.
	s.mux.HandleFunc("/api/v1/oidc/login", s.handleOIDCLogin)
	s.mux.HandleFunc("/api/v1/oidc/callback", s.handleOIDCCallback)
	// Post-logout landing — IamBarn redirects here after end-session; clears
	// the local session cookies. Public: it runs while tearing a session down.
	s.mux.HandleFunc("/api/v1/oidc/logout-complete", s.handleOIDCLogoutComplete)
	// Back-channel logout — public (the IdP is the caller; authenticity comes
	// from the signed logout token) but rate-limited under the login bucket.
	s.mux.Handle("/api/v1/oidc/backchannel-logout",
		RateLimitMiddleware(env.rl, "login")(http.HandlerFunc(s.handleBackchannelLogout)))
	// Forced session refresh — POST so split deployments route it to the
	// writer (readers mount SQLite read-only and cannot persist rotations).
	s.mux.Handle("/api/v1/session/refresh", env.apiRL(http.HandlerFunc(s.handleSessionRefresh)))
}

// registerIngestRoutes mounts the endpoints that accept telemetry.
func (s *Server) registerIngestRoutes(env *routeEnv) {
	ingestRL, ingestAuth, otlpAuth := env.ingestRL, env.ingestAuth, env.otlpAuth

	// Internal ingest endpoint — used by ingest pods to forward spans to writer.
	// Uses raw API key auth (pod-to-pod, no need for SHA256/DB lookup).
	if s.ingest != nil {
		internalAuth := func(next http.Handler) http.Handler { return apiKeyOrBearerAuth(s.apiKey, next) }
		s.mux.Handle("/internal/v1/ingest", internalAuth(http.HandlerFunc(s.handleInternalIngest)))
	}

	// shed refuses telemetry while the storage volume is nearly full. It sits
	// *inside* auth deliberately: capacity state is internal, so an
	// unauthenticated caller should get 401 rather than learn that our disk is
	// filling. The auth lookup it costs is a read, which still succeeds on a
	// full volume — only writes fail.
	//
	// It is applied to telemetry routes only, never to the query or session
	// routes: the entire point is that the dashboard and login survive the
	// condition that makes ingest unsafe. /internal/v1/ingest is also left
	// ungated — it is the pod-to-pod forwarding path, and refusing there would
	// strand records in the sender's spool, which rotates and drops them.
	shed := s.admission.Middleware()
	if s.ingest != nil {
		s.mux.Handle("/api/v1/spans", ingestRL(ingestAuth(shed(http.HandlerFunc(s.handleIngest)))))
		// OTLP/HTTP trace endpoint — only registered when there is an ingest handler.
		s.mux.Handle("/v1/traces", ingestRL(otlpAuth(shed(http.HandlerFunc(s.handleOTLP)))))
	}
	if s.metricsIngest != nil {
		// OTLP/HTTP metrics endpoint.
		s.mux.Handle("/v1/metrics", ingestRL(otlpAuth(shed(http.HandlerFunc(s.handleOTLPMetrics)))))
	}
	if s.logsIngest != nil {
		// OTLP/HTTP logs endpoint.
		s.mux.Handle("/v1/logs", ingestRL(otlpAuth(shed(http.HandlerFunc(s.handleOTLPLogs)))))
	}

	// Frontend telemetry — session auth, accepts same format as /api/v1/spans.
	if s.ingest != nil && s.sessions != nil {
		s.mux.Handle("/api/v1/telemetry", ingestRL(env.sessionAuth(http.HandlerFunc(s.handleIngest))))
	}

	// E2E session endpoint — API key auth required; only works when e2e_enabled.
	if s.repo != nil && s.sessions != nil && s.authorizer != nil {
		s.mux.Handle("/api/v1/e2e/session", ingestRL(ingestAuth(http.HandlerFunc(s.handleE2ESession))))
	}
}

// registerQueryRoutes mounts the read endpoints: traces and services, live
// tail, metrics and logs.
func (s *Server) registerQueryRoutes(env *routeEnv) {
	sessionAuth := env.sessionAuth

	// Query endpoints — rate limited + session auth required.
	// List/aggregate endpoints get a short cache (30s); detail endpoints are not cached.
	if s.querySvc != nil && s.sessions != nil {
		s.registerTraceQueryRoutes(env)
	}

	// Live tail SSE endpoint — session auth required.
	if s.ingest != nil && s.sessions != nil {
		lth := &liveTailHandler{broadcaster: s.ingest.Broadcaster()}
		s.mux.Handle("/api/v1/spans/live", sessionAuth(lth))
	}

	if s.repo != nil && s.sessions != nil {
		s.registerMetricsAndLogsRoutes(env)
	}
}

// registerTraceQueryRoutes mounts the trace, service, dashboard and prompt
// query endpoints served by the query service.
func (s *Server) registerTraceQueryRoutes(env *routeEnv) {
	apiRL, readAuth, sessionAuth := env.apiRL, env.readAuth, env.sessionAuth
	qh := &queryHandlers{svc: s.querySvc}
	cache60 := func(h http.Handler) http.Handler { return cacheMiddleware(60, h) }

	s.mux.Handle("/api/v1/services", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleServices)))))
	s.mux.Handle("/api/v1/services/", apiRL(readAuth(cache60(qh))))
	s.mux.Handle("/api/v1/traces", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleTraces)))))
	s.mux.Handle("/api/v1/spans/search", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleSpanSearch)))))
	s.mux.Handle("/api/v1/traces/groups", apiRL(readAuth(http.HandlerFunc(qh.handleTraceGroups))))
	s.mux.Handle("/api/v1/traces/", apiRL(readAuth(http.HandlerFunc(qh.handleTraceDetail))))
	s.mux.Handle("/api/v1/trace-health/orphan-spans", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleOrphanSpans)))))
	s.mux.Handle("/api/v1/trace-health/rootless-traces", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleRootlessTraces)))))
	s.mux.Handle("/api/v1/trace-health/single-span-traces", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleSingleSpanTraces)))))
	s.mux.Handle("/api/v1/trace-health/span-names", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleSpanNames)))))
	s.mux.Handle("/api/v1/attributes", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleAttributes)))))
	s.mux.Handle("/api/v1/attributes/compare", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleAttributeCompare)))))
	s.mux.Handle("/api/v1/heatmap", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleHeatmap)))))
	s.mux.Handle("/api/v1/analyze", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleAnalyze)))))
	s.mux.Handle("/api/v1/analyze/series", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleAnalyzeSeries)))))
	s.mux.Handle("/api/v1/dependencies", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDependencies)))))
	s.mux.Handle("/api/v1/database", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDatabaseQueries)))))
	s.mux.Handle("/api/v1/database/detail", apiRL(readAuth(http.HandlerFunc(qh.handleDatabaseQueryDetail))))
	s.mux.Handle("/api/v1/prompts", apiRL(readAuth(cache60(http.HandlerFunc(qh.handlePrompts)))))
	s.mux.Handle("/api/v1/prompts/detail", apiRL(readAuth(http.HandlerFunc(qh.handlePromptDetail))))
	s.mux.Handle("/api/v1/service-map", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleServiceMap)))))
	s.mux.Handle("/api/v1/dashboard/counts", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDashboardCounts)))))
	s.mux.Handle("/api/v1/dashboard/percentiles", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDashboardPercentiles)))))
	s.mux.Handle("/api/v1/dashboard/heatmap", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDashboardHeatmap)))))
	// Web vitals are RUM aggregates that the query service does not scope by
	// project, so they stay session-only (not exposed by the read-key CLI).
	s.mux.Handle("/api/v1/web-vitals", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitals)))))
	s.mux.Handle("/api/v1/web-vitals/timeseries", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitalsTimeseries)))))
}

// registerManagementRoutes mounts the session-authenticated endpoints for
// alerts, settings, saved queries, boards, trace exclusions and export.
func (s *Server) registerManagementRoutes(env *routeEnv) {
	if s.repo == nil || s.sessions == nil {
		return
	}
	apiRL, sessionAuth := env.apiRL, env.sessionAuth

	// Alert endpoints — rate limited + session auth required.
	ah := &alertHandlers{svc: service.NewAlertService(s.repo)}
	s.mux.Handle("/api/v1/alerts", apiRL(sessionAuth(ah)))
	s.mux.Handle("/api/v1/alerts/", apiRL(sessionAuth(ah)))

	// SLOs, burn alerts and status — rate limited + session auth required.
	slh := &sloHandlers{svc: service.NewSLOService(s.repo, slog.Default())}
	slh.register(s.mux, func(h http.Handler) http.Handler { return apiRL(sessionAuth(h)) })

	// Settings + stats endpoints — rate limited + session auth required.
	sh := &settingsHandlers{svc: service.NewSettingsService(s.repo), dbPath: s.dbPath, spoolDir: s.spoolDir, cache: s.cache}
	s.mux.Handle("/api/v1/settings", apiRL(sessionAuth(sh)))
	s.mux.Handle("/api/v1/stats/db-size", apiRL(sessionAuth(sh)))
	s.mux.Handle("/api/v1/stats/counts", apiRL(sessionAuth(sh)))
	s.mux.Handle("/api/v1/stats/runtime", apiRL(sessionAuth(sh)))

	// Saved queries endpoints — rate limited + session auth required.
	sqh := &savedQueryHandlers{svc: service.NewSavedQueryService(s.repo)}
	s.mux.Handle("/api/v1/saved-queries", apiRL(sessionAuth(sqh)))
	s.mux.Handle("/api/v1/saved-queries/", apiRL(sessionAuth(sqh)))

	// Boards and release markers — rate limited + session auth required.
	bh := &boardHandlers{svc: service.NewBoardService(s.repo, slog.Default())}
	bh.register(s.mux, func(h http.Handler) http.Handler { return apiRL(sessionAuth(h)) })

	// Calculated fields — per-project expressions usable as filter and group-by keys.
	newCalculatedFieldHandlers(s.repo).register(s.mux, func(h http.Handler) http.Handler { return apiRL(sessionAuth(h)) })

	// Trace exclusions — persistent operation-level filters per project.
	teh := &traceExclusionHandlers{svc: service.NewTraceExclusionService(s.repo)}
	s.mux.Handle("/api/v1/trace-exclusions", apiRL(sessionAuth(teh)))
	s.mux.Handle("/api/v1/trace-exclusions/", apiRL(sessionAuth(teh)))

	// Export endpoint — rate limited + session auth required, streams NDJSON.
	eh := &exportHandlers{svc: service.NewExportService(s.repo)}
	s.mux.Handle("/api/v1/export", apiRL(sessionAuth(eh)))
}

// registerProjectRoutes mounts the project endpoints: read auth (session or
// read key) for listing; the middleware blocks mutating methods for API keys.
func (s *Server) registerProjectRoutes(env *routeEnv) {
	if s.repo == nil || s.sessions == nil {
		return
	}
	ph := &projectHandlers{svc: s.projectService(), settings: service.NewSettingsService(s.repo), cache: s.cache}
	s.mux.Handle("/api/v1/projects", env.apiRL(env.readAuth(ph)))
	s.mux.Handle("/api/v1/projects/", env.apiRL(env.readAuth(ph)))
}

// registerMetricsAndLogsRoutes mounts the metrics and logs query endpoints.
// Metrics are session-only; logs accept a read key, except pinned traces, which
// are per-user state and stay session-only.
func (s *Server) registerMetricsAndLogsRoutes(env *routeEnv) {
	mqh := &metricsQueryHandlers{svc: service.NewMetricsService(s.repo)}
	lqh := &logsQueryHandlers{svc: service.NewLogsService(s.repo)}
	routes := []struct {
		path string
		auth func(http.Handler) http.Handler
		h    http.HandlerFunc
	}{
		{"/api/v1/metrics/names", env.readAuth, mqh.handleMetricNames},
		{"/api/v1/metrics/catalog", env.readAuth, mqh.handleMetricCatalog},
		{"/api/v1/metrics/insights", env.readAuth, mqh.handleMetricInsights},
		{"/api/v1/metrics/series", env.readAuth, mqh.handleMetricSeries},
		{"/api/v1/logs", env.readAuth, lqh.handleLogs},
		{"/api/v1/logs/histogram", env.readAuth, lqh.handleLogsHistogram},
		{"/api/v1/pinned-traces", env.sessionAuth, lqh.handlePinnedTraces},
		{"/api/v1/pinned-traces/", env.sessionAuth, lqh.handlePinnedTraces},
	}
	for _, r := range routes {
		s.mux.Handle(r.path, env.apiRL(r.auth(r.h)))
	}
}
