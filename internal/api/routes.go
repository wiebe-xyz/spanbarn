package api

import "net/http"

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	rl := s.rateLimiter
	ingestRL := RateLimitMiddleware(rl, "ingest")
	apiRL := RateLimitMiddleware(rl, "api")

	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

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
	s.mux.Handle("/api/v1/spans", ingestRL(ingestAuth(http.HandlerFunc(s.handleIngest))))

	// OTLP/HTTP endpoint — rate limited + API key or Bearer auth required.
	s.mux.Handle("/v1/traces", ingestRL(otlpAuth(http.HandlerFunc(s.handleOTLP))))

	// Query endpoints — rate limited + session auth required.
	if s.querySvc != nil && s.sessionMgr != nil {
		qh := &queryHandlers{svc: s.querySvc}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/services", apiRL(sessionAuth(http.HandlerFunc(qh.handleServices))))
		s.mux.Handle("/api/v1/services/", apiRL(sessionAuth(qh)))
		s.mux.Handle("/api/v1/traces", apiRL(sessionAuth(http.HandlerFunc(qh.handleTraces))))
		s.mux.Handle("/api/v1/traces/", apiRL(sessionAuth(qh)))
		s.mux.Handle("/api/v1/dependencies", apiRL(sessionAuth(http.HandlerFunc(qh.handleDependencies))))
		s.mux.Handle("/api/v1/database", apiRL(sessionAuth(http.HandlerFunc(qh.handleDatabaseQueries))))
		s.mux.Handle("/api/v1/database/detail", apiRL(sessionAuth(http.HandlerFunc(qh.handleDatabaseQueryDetail))))
		s.mux.Handle("/api/v1/prompts", apiRL(sessionAuth(http.HandlerFunc(qh.handlePrompts))))
		s.mux.Handle("/api/v1/prompts/detail", apiRL(sessionAuth(http.HandlerFunc(qh.handlePromptDetail))))
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
		sh := &settingsHandlers{repo: s.repo, dbPath: s.dbPath, spoolDir: s.spoolDir}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/settings", apiRL(sessionAuth(sh)))
		s.mux.Handle("/api/v1/stats", apiRL(sessionAuth(sh)))
	}

	// Setup endpoint — public, no auth required.
	if s.repo != nil {
		s.mux.HandleFunc("/api/v1/setup/{slug}", s.handleSetup)
	}

	// Project endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		ph := &projectHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/projects", apiRL(sessionAuth(ph)))
		s.mux.Handle("/api/v1/projects/", apiRL(sessionAuth(ph)))
	}

}
