package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// handleE2ESession issues a browser session for an E2E test account without
// requiring the OIDC flow. The request must be authenticated with a project
// API key, and the project must have e2e_enabled = true.
//
// A dedicated user account named "e2e:<project-slug>" is created (or its
// expiry refreshed) and a normal session cookie is set. The account is
// automatically deleted after repository.E2EAccountTTL (7 days) by the
// retention worker, which also invalidates any outstanding sessions because
// subsequent session renewals would fail to find the user.
func (s *Server) handleE2ESession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	if s.sessionMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}

	projectID := GetProjectID(r.Context())
	project, err := s.repo.GetProjectByID(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load project", "")
		return
	}
	if !project.E2EEnabled {
		writeError(w, http.StatusForbidden, "e2e mode is not enabled for this project", "")
		return
	}

	username := fmt.Sprintf("e2e:%s", project.Slug)
	expiresAt := time.Now().UTC().Add(repository.E2EAccountTTL)

	if _, err := s.repo.UpsertE2EUser(username, expiresAt); err != nil {
		s.logger.Error("e2e: upsert user", "username", username, "error", err)
		writeError(w, http.StatusInternalServerError, "could not create e2e account", "")
		return
	}

	token, err := s.sessionMgr.Create(username)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}

	secure := r.TLS != nil
	sessionExpires := time.Now().Add(s.sessionMgr.TTL())
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		Expires:  sessionExpires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "spanbarn_auth_method",
		Value:    "e2e",
		Path:     "/",
		Expires:  sessionExpires,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"username":          username,
		"account_expires_at": expiresAt.Format(time.RFC3339),
		"session_expires_at": sessionExpires.Format(time.RFC3339),
	})
}
