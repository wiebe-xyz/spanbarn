package api

import "net/http"

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	rl := s.rateLimiter
	ingestRL := RateLimitMiddleware(rl, "ingest")
	apiRL := RateLimitMiddleware(rl, "api")

	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

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
	}

	// Alert endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		ah := &alertHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/alerts", apiRL(sessionAuth(ah)))
		s.mux.Handle("/api/v1/alerts/", apiRL(sessionAuth(ah)))
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
