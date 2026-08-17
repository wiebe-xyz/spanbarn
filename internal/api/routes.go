package api

import "net/http"

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	rl := s.rateLimiter
	ingestRL := RateLimitMiddleware(rl, "ingest")
	apiRL := RateLimitMiddleware(rl, "api")

	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

	// Client-config endpoint — public, no auth. Exposes only non-secret values
	// (e.g. funnelbarn project + ingest API key) that the SPA needs at boot.
	s.mux.HandleFunc("/api/v1/client-config", s.handleClientConfig)

	// Me endpoint — session auth required. Returns the current user's display
	// name so the SPA can show it in the profile chip without a cross-origin
	// request to IamBarn.
	if s.sessions != nil {
		sessionAuth := SessionMiddleware(s.sessions)
		s.mux.Handle("/api/v1/me", apiRL(sessionAuth(http.HandlerFunc(s.handleMe))))
	}

	// IamBarn proxy — session auth required when OIDC is configured. Forwards
	// iambarn-profile widget requests to IamBarn with Bearer auth so the
	// widget works same-origin (no cross-site cookie issues).
	// Must be under /api/ so Caddy routes it to the Go service.
	// Registered unconditionally — handleIAMProxy returns 404 when OIDC is
	// not configured. (SetOIDCClient runs after registerRoutes, so we cannot
	// gate registration on s.oidc != nil here.)
	if s.sessions != nil {
		sessionAuth := SessionMiddleware(s.sessions)
		s.mux.Handle("/api/iam-proxy/", sessionAuth(http.HandlerFunc(s.handleIAMProxy)))
	}

	// IAMBarn theme manifest — public, no auth, no redirects. Served at the
	// well-known path so IAMBarn can adopt SpanBarn's brand on its login page
	// when users arrive via an OAuth authorize redirect from this host.
	s.mux.HandleFunc("/.well-known/iambarn-theme.json", s.handleThemeManifest)

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
		RateLimitMiddleware(rl, "login")(http.HandlerFunc(s.handleBackchannelLogout)))
	// Forced session refresh — POST so split deployments route it to the
	// writer (readers mount SQLite read-only and cannot persist rotations).
	s.mux.Handle("/api/v1/session/refresh", apiRL(http.HandlerFunc(s.handleSessionRefresh)))

	// Internal ingest endpoint — used by ingest pods to forward spans to writer.
	// Uses raw API key auth (pod-to-pod, no need for SHA256/DB lookup).
	if s.ingest != nil {
		internalAuth := func(next http.Handler) http.Handler { return apiKeyOrBearerAuth(s.apiKey, next) }
		s.mux.Handle("/internal/v1/ingest", internalAuth(http.HandlerFunc(s.handleInternalIngest)))
	}

	// Metrics endpoint.
	if s.metrics != nil {
		s.mux.Handle("/metrics", s.metrics.Handler(s.metricsToken))
	}

	// Ingest endpoint — rate limited + API key auth required.
	var ingestAuth, otlpAuth func(http.Handler) http.Handler
	if s.authorizer != nil {
		ingestAuth = func(next http.Handler) http.Handler { return authorizerOrBearerAuth(s.authorizer, next) }
		otlpAuth = ingestAuth
	} else {
		ingestAuth = func(next http.Handler) http.Handler { return apiKeyAuth(s.apiKey, next) }
		otlpAuth = func(next http.Handler) http.Handler { return apiKeyOrBearerAuth(s.apiKey, next) }
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

	// Query endpoints — rate limited + session auth required.
	// List/aggregate endpoints get a short cache (30s); detail endpoints are not cached.
	if s.querySvc != nil && s.sessions != nil {
		qh := &queryHandlers{svc: s.querySvc}
		readAuth := SessionOrReadKey(s.sessions, s.authorizer, s.oidcClient)
		sessionAuth := SessionMiddleware(s.sessions)
		cache60 := func(h http.Handler) http.Handler { return cacheMiddleware(60, h) }

		s.mux.Handle("/api/v1/services", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleServices)))))
		s.mux.Handle("/api/v1/services/", apiRL(readAuth(cache60(qh))))
		s.mux.Handle("/api/v1/traces", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleTraces)))))
		s.mux.Handle("/api/v1/traces/groups", apiRL(readAuth(http.HandlerFunc(qh.handleTraceGroups))))
		s.mux.Handle("/api/v1/traces/", apiRL(readAuth(http.HandlerFunc(qh.handleTraceDetail))))
		s.mux.Handle("/api/v1/dependencies", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDependencies)))))
		s.mux.Handle("/api/v1/database", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleDatabaseQueries)))))
		s.mux.Handle("/api/v1/database/detail", apiRL(readAuth(http.HandlerFunc(qh.handleDatabaseQueryDetail))))
		s.mux.Handle("/api/v1/prompts", apiRL(readAuth(cache60(http.HandlerFunc(qh.handlePrompts)))))
		s.mux.Handle("/api/v1/prompts/detail", apiRL(readAuth(http.HandlerFunc(qh.handlePromptDetail))))
		s.mux.Handle("/api/v1/service-map", apiRL(readAuth(cache60(http.HandlerFunc(qh.handleServiceMap)))))
		// Web vitals are RUM aggregates that the query service does not scope by
		// project, so they stay session-only (not exposed by the read-key CLI).
		s.mux.Handle("/api/v1/web-vitals", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitals)))))
		s.mux.Handle("/api/v1/web-vitals/timeseries", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitalsTimeseries)))))
	}

	// Live tail SSE endpoint — session auth required.
	if s.ingest != nil && s.sessions != nil {
		lth := &liveTailHandler{broadcaster: s.ingest.Broadcaster()}
		sessionAuth := SessionMiddleware(s.sessions)
		s.mux.Handle("/api/v1/spans/live", sessionAuth(lth))
	}

	// Metrics query endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessions != nil {
		mqh := &metricsQueryHandlers{repo: s.repo}
		readAuth := SessionOrReadKey(s.sessions, s.authorizer, s.oidcClient)

		s.mux.Handle("/api/v1/metrics/names", apiRL(readAuth(http.HandlerFunc(mqh.handleMetricNames))))
		s.mux.Handle("/api/v1/metrics/catalog", apiRL(readAuth(http.HandlerFunc(mqh.handleMetricCatalog))))
		s.mux.Handle("/api/v1/metrics/insights", apiRL(readAuth(http.HandlerFunc(mqh.handleMetricInsights))))
		s.mux.Handle("/api/v1/metrics/series", apiRL(readAuth(http.HandlerFunc(mqh.handleMetricSeries))))
	}

	// Logs query endpoints — read auth (session or read key). Pinned-traces are
	// per-user state, so they stay session-only.
	if s.repo != nil && s.sessions != nil {
		lqh := &logsQueryHandlers{repo: s.repo}
		readAuth := SessionOrReadKey(s.sessions, s.authorizer, s.oidcClient)
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/logs", apiRL(readAuth(http.HandlerFunc(lqh.handleLogs))))
		s.mux.Handle("/api/v1/logs/histogram", apiRL(readAuth(http.HandlerFunc(lqh.handleLogsHistogram))))
		s.mux.Handle("/api/v1/pinned-traces", apiRL(sessionAuth(http.HandlerFunc(lqh.handlePinnedTraces))))
		s.mux.Handle("/api/v1/pinned-traces/", apiRL(sessionAuth(http.HandlerFunc(lqh.handlePinnedTraces))))
	}

	// Alert endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessions != nil {
		ah := &alertHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/alerts", apiRL(sessionAuth(ah)))
		s.mux.Handle("/api/v1/alerts/", apiRL(sessionAuth(ah)))
	}

	// Settings + stats endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessions != nil {
		sh := &settingsHandlers{repo: s.repo, dbPath: s.dbPath, spoolDir: s.spoolDir, cache: s.cache}
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/settings", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/db-size", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/counts", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/runtime", apiRL(sessionAuth(sh)))
	}

	// Saved queries endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessions != nil {
		sqh := &savedQueryHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/saved-queries", apiRL(sessionAuth(sqh)))
		s.mux.Handle("/api/v1/saved-queries/", apiRL(sessionAuth(sqh)))
	}

	// Trace exclusions — persistent operation-level filters per project.
	if s.repo != nil && s.sessions != nil {
		teh := &traceExclusionHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/trace-exclusions", apiRL(sessionAuth(teh)))
		s.mux.Handle("/api/v1/trace-exclusions/", apiRL(sessionAuth(teh)))
	}

	// Frontend telemetry — session auth, accepts same format as /api/v1/spans.
	if s.ingest != nil && s.sessions != nil {
		sessionAuth := SessionMiddleware(s.sessions)
		s.mux.Handle("/api/v1/telemetry", ingestRL(sessionAuth(http.HandlerFunc(s.handleIngest))))
		s.mux.Handle("/api/v1/client-errors", ingestRL(sessionAuth(http.HandlerFunc(s.handleClientError))))
	}

	// Export endpoint — rate limited + session auth required, streams NDJSON.
	if s.repo != nil && s.sessions != nil {
		eh := &exportHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessions)

		s.mux.Handle("/api/v1/export", apiRL(sessionAuth(eh)))
	}

	// Setup endpoint — intentionally public (onboarding), but rate-limited and
	// GET-only + read-idempotent (see handleSetup) so anonymous callers cannot
	// use it to spam projects or amplify writes.
	if s.repo != nil {
		s.mux.Handle("/api/v1/setup/{slug}", apiRL(http.HandlerFunc(s.handleSetup)))
	}

	// E2E session endpoint — API key auth required; only works when e2e_enabled.
	if s.repo != nil && s.sessions != nil && s.authorizer != nil {
		s.mux.Handle("/api/v1/e2e/session", ingestRL(ingestAuth(http.HandlerFunc(s.handleE2ESession))))
	}

	// Project endpoints — read auth (session or read key) for listing; the
	// middleware blocks mutating methods for API keys.
	if s.repo != nil && s.sessions != nil {
		ph := &projectHandlers{repo: s.repo, cache: s.cache}
		readAuth := SessionOrReadKey(s.sessions, s.authorizer, s.oidcClient)

		s.mux.Handle("/api/v1/projects", apiRL(readAuth(ph)))
		s.mux.Handle("/api/v1/projects/", apiRL(readAuth(ph)))
	}

}
