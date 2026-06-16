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
// handleMe returns the display name for the currently authenticated session.
// This is a lightweight same-origin endpoint so the frontend can show the
// user's name without a cross-origin request to IamBarn.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	username := GetUsername(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"display_name": username})
}

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
		oc := s.oidc.Config()
		issuer := strings.TrimRight(oc.Issuer, "/")
		oidcCfg := map[string]any{
			"enabled":  true,
			"loginURL": "/api/v1/oidc/login",
			"issuer":   issuer,
		}
		// The sb CLI uses this public client for the device-code login flow.
		if oc.CLIClientID != "" {
			oidcCfg["cli_client_id"] = oc.CLIClientID
		}
		resp["oidc"] = oidcCfg
		if issuer != "" {
			resp["iambarn"] = map[string]string{
				"issuer":      issuer,
				"profile_url": issuer + "/admin#profile",
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
