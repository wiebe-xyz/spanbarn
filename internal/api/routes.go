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
	ingestHandler := ingestRL(apiKeyAuth(s.apiKey, http.HandlerFunc(s.handleIngest)))
	s.mux.Handle("/api/v1/spans", ingestHandler)

	// OTLP/HTTP endpoint — rate limited + API key or Bearer auth required.
	otlpHandler := ingestRL(apiKeyOrBearerAuth(s.apiKey, http.HandlerFunc(s.handleOTLP)))
	s.mux.Handle("/v1/traces", otlpHandler)

	// Query endpoints — rate limited + session auth required.
	if s.querySvc != nil && s.sessionMgr != nil {
		qh := &queryHandlers{svc: s.querySvc}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/services", apiRL(sessionAuth(http.HandlerFunc(qh.handleServices))))
		s.mux.Handle("/api/v1/services/", apiRL(sessionAuth(qh)))
		s.mux.Handle("/api/v1/traces", apiRL(sessionAuth(http.HandlerFunc(qh.handleTraces))))
		s.mux.Handle("/api/v1/traces/", apiRL(sessionAuth(qh)))
		s.mux.Handle("/api/v1/dependencies", apiRL(sessionAuth(http.HandlerFunc(qh.handleDependencies))))
	}

	// Alert endpoints — rate limited + session auth required.
	if s.repo != nil && s.sessionMgr != nil {
		ah := &alertHandlers{repo: s.repo}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		s.mux.Handle("/api/v1/alerts", apiRL(sessionAuth(ah)))
		s.mux.Handle("/api/v1/alerts/", apiRL(sessionAuth(ah)))
	}

}
