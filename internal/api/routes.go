package api

import "net/http"

// registerRoutes sets up all HTTP routes on the server's mux.
func (s *Server) registerRoutes() {
	// Health endpoint — no auth required.
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)

	// Ingest endpoint — API key auth required.
	ingestHandler := apiKeyAuth(s.apiKey, http.HandlerFunc(s.handleIngest))
	s.mux.Handle("/api/v1/spans", ingestHandler)
}
