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
// handleMe returns the display name — and, for OIDC sessions, the profile
// snapshot (name/email/picture) from the session row's claims — for the
// currently authenticated session. This is a lightweight same-origin endpoint
// so the frontend can render the header chip without a cross-origin request
// to IamBarn (it replaced the JS-readable spanbarn_iam_profile cookie).
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"display_name": GetUsername(r.Context())}
	if ws, ok := GetWebSession(r.Context()); ok {
		resp["auth_method"] = ws.AuthMethod
		if ws.ClaimsJSON != "" {
			var claims struct {
				Name    string `json:"name"`
				Email   string `json:"email"`
				Picture string `json:"picture"`
			}
			if err := json.Unmarshal([]byte(ws.ClaimsJSON), &claims); err == nil {
				name := claims.Name
				if name == "" {
					name = ws.Username
				}
				resp["profile"] = map[string]string{
					"name":    name,
					"email":   claims.Email,
					"picture": claims.Picture,
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
			iambarnCfg := map[string]string{
				"issuer":      issuer,
				"profile_url": issuer + "/admin#profile",
			}
			// Non-secret values the hosted widgets + logout flow need in the
			// browser: the client id and the post-logout landing URL.
			if oc.ClientID != "" {
				iambarnCfg["client_id"] = oc.ClientID
			}
			if oc.PostLogoutRedirectURI != "" {
				iambarnCfg["post_logout_redirect_uri"] = oc.PostLogoutRedirectURI
			}
			resp["iambarn"] = iambarnCfg
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
