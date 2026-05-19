package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(HealthResponse{
		Status:  "ok",
		Version: s.version,
	})
}

// handleClientConfig returns the public runtime config the SPA needs at boot.
// Only non-secret values that the browser is expected to send back upstream are
// included. When an integration is not configured server-side, its block is
// omitted so the SPA can no-op cleanly.
func (s *Server) handleClientConfig(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{}
	if s.funnelBarn.Endpoint != "" {
		resp["funnelbarn"] = map[string]string{
			"endpoint": s.funnelBarn.Endpoint,
			"api_key":  s.funnelBarn.APIKey,
			"project":  s.funnelBarn.Project,
		}
	}
	if s.oidc != nil {
		resp["oidc"] = map[string]any{
			"enabled":  true,
			"loginURL": "/api/v1/oidc/login",
		}
		if issuer := strings.TrimRight(s.oidc.Config().Issuer, "/"); issuer != "" {
			resp["iambarn"] = map[string]string{
				"profile_url": issuer + "/admin#profile",
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
