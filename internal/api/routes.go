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
	if s.sessionMgr != nil {
		sessionAuth := SessionMiddleware(s.sessionMgr)
		s.mux.Handle("/api/v1/me", apiRL(sessionAuth(http.HandlerFunc(s.handleMe))))
	}

	// IAMBarn theme manifest — public, no auth, no redirects. Served at the
	// well-known path so IAMBarn can adopt SpanBarn's brand on its login page
	// when users arrive via an OAuth authorize redirect from this host.
	s.mux.HandleFunc("/.well-known/iambarn-theme.json", s.handleThemeManifest)

	// OIDC login flow — public, no auth required. Returns 404 when OIDC is not
	// configured server-side, so the SPA can fall through to local login.
	s.mux.HandleFunc("/api/v1/oidc/login", s.handleOIDCLogin)
	s.mux.HandleFunc("/api/v1/oidc/callback", s.handleOIDCCallback)

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
	if s.ingest != nil {
		s.mux.Handle("/api/v1/spans", ingestRL(ingestAuth(http.HandlerFunc(s.handleIngest))))
		// OTLP/HTTP endpoint — only registered when there is an ingest handler.
		s.mux.Handle("/v1/traces", ingestRL(otlpAuth(http.HandlerFunc(s.handleOTLP))))
	}

	// Query endpoints — rate limited + session auth required.
	// List/aggregate endpoints get a short cache (30s); detail endpoints are not cached.
	if s.querySvc != nil && s.sessionMgr != nil {
		qh := &queryHandlers{svc: s.querySvc}
		sessionAuth := SessionMiddleware(s.sessionMgr)
		cache60 := func(h http.Handler) http.Handler { return cacheMiddleware(60, h) }

		s.mux.Handle("/api/v1/services", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleServices)))))
		s.mux.Handle("/api/v1/services/", apiRL(sessionAuth(cache60(qh))))
		s.mux.Handle("/api/v1/traces", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleTraces)))))
		s.mux.Handle("/api/v1/traces/groups", apiRL(sessionAuth(http.HandlerFunc(qh.handleTraceGroups))))
		s.mux.Handle("/api/v1/traces/", apiRL(sessionAuth(http.HandlerFunc(qh.handleTraceDetail))))
		s.mux.Handle("/api/v1/dependencies", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleDependencies)))))
		s.mux.Handle("/api/v1/database", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleDatabaseQueries)))))
		s.mux.Handle("/api/v1/database/detail", apiRL(sessionAuth(http.HandlerFunc(qh.handleDatabaseQueryDetail))))
		s.mux.Handle("/api/v1/prompts", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handlePrompts)))))
		s.mux.Handle("/api/v1/prompts/detail", apiRL(sessionAuth(http.HandlerFunc(qh.handlePromptDetail))))
		s.mux.Handle("/api/v1/service-map", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleServiceMap)))))
		s.mux.Handle("/api/v1/web-vitals", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitals)))))
		s.mux.Handle("/api/v1/web-vitals/timeseries", apiRL(sessionAuth(cache60(http.HandlerFunc(qh.handleWebVitalsTimeseries)))))
	}

	// Live tail SSE endpoint — session auth required.
	if s.ingest != nil && s.sessionMgr != nil {
		lth := &liveTailHandler{broadcaster: s.ingest.Broadcaster()}
		sessionAuth := SessionMiddleware(s.sessionMgr)
		s.mux.Handle("/api/v1/spans/live", sessionAuth(lth))
	}

	// Alert endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		ah := &alertHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/alerts", apiRL(sessionAuth(ah)))
		s.mux.Handle("/api/v1/alerts/", apiRL(sessionAuth(ah)))
	}

	// Settings + stats endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		sh := &settingsHandlers{repo: s.repo, dbPath: s.dbPath, spoolDir: s.spoolDir, cache: s.cache}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/settings", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/db-size", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/counts", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats/runtime", apiRL(sessionAuth(sh)))
	}

	// Saved queries endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		sqh := &savedQueryHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/saved-queries", apiRL(sessionAuth(sqh)))
		s.mux.Handle("/api/v1/saved-queries/", apiRL(sessionAuth(sqh)))
	}

	// Trace exclusions — persistent operation-level filters per project.
	if s.repo != nil && s.sessionMgr != nil {
		teh := &traceExclusionHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/trace-exclusions", apiRL(sessionAuth(teh)))
		s.mux.Handle("/api/v1/trace-exclusions/", apiRL(sessionAuth(teh)))
	}

	// Frontend telemetry — session auth, accepts same format as /api/v1/spans.
	if s.ingest != nil && s.sessionMgr != nil {
		sessionAuth := SessionMiddleware(s.sessionMgr)
		s.mux.Handle("/api/v1/telemetry", ingestRL(sessionAuth(http.HandlerFunc(s.handleIngest))))
		s.mux.Handle("/api/v1/client-errors", ingestRL(sessionAuth(http.HandlerFunc(s.handleClientError))))
	}

	// Export endpoint — rate limited + session auth required, streams NDJSON.
	if s.repo != nil && s.sessionMgr != nil {
		eh := &exportHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/export", apiRL(sessionAuth(eh)))
	}

	// Setup endpoint — public, no auth required.
	if s.repo != nil {
		s.mux.HandleFunc("/api/v1/setup/{slug}", s.handleSetup)
	}

	// E2E session endpoint — API key auth required; only works when e2e_enabled.
	if s.repo != nil && s.sessionMgr != nil && s.authorizer != nil {
		s.mux.Handle("/api/v1/e2e/session", ingestRL(ingestAuth(http.HandlerFunc(s.handleE2ESession))))
	}

	// Project endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		ph := &projectHandlers{repo: s.repo, cache: s.cache}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/projects", apiRL(sessionAuth(ph)))
		s.mux.Handle("/api/v1/projects/", apiRL(sessionAuth(ph)))
	}

}
