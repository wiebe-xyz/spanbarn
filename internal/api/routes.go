package api

import "net/http"

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

	// Ingest endpoint — API key auth required.
	ingestHandler := apiKeyAuth(s.apiKey, http.HandlerFunc(s.handleIngest))
	s.mux.Handle("/api/v1/spans", ingestHandler)

	// OTLP/HTTP endpoint — API key or Bearer auth required.
	otlpHandler := apiKeyOrBearerAuth(s.apiKey, http.HandlerFunc(s.handleOTLP))
	s.mux.Handle("/v1/traces", otlpHandler)

	// Query endpoints — session auth required.
	if s.querySvc != nil && s.sessionMgr != nil {
		qh := &queryHandlers{svc: s.querySvc}
		sessionAuth := SessionMiddleware(s.sessionMgr)

		// Use a single catch-all for /api/v1/services/ with trailing slash
		// and the exact /api/v1/services path, plus traces and dependencies.
		s.mux.Handle("/api/v1/services", sessionAuth(http.HandlerFunc(qh.handleServices)))
		s.mux.Handle("/api/v1/services/", sessionAuth(qh))
		s.mux.Handle("/api/v1/traces", sessionAuth(http.HandlerFunc(qh.handleTraces)))
		s.mux.Handle("/api/v1/traces/", sessionAuth(qh))
		s.mux.Handle("/api/v1/dependencies", sessionAuth(http.HandlerFunc(qh.handleDependencies)))
	}
}
