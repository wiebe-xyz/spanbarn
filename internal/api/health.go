package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(HealthResponse{
		Status:  "ok",
		Version: s.version,
	})
}
