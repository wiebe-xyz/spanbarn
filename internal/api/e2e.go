package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// handleE2ESession issues a browser session for an E2E test account without
// requiring the OIDC flow. It is disabled entirely in production.
//
// Two modes of authentication are accepted:
//
//  1. Project API key — the project must have e2e_enabled = true. The session
//     is created for a user named "e2e:<project-slug>".
//
//  2. Static admin key (project ID 0 / scope "full") — bypasses the
//     e2e_enabled flag. Intended for CI pipelines on non-production instances.
//
// The E2E account expires after repository.E2EAccountTTL (7 days) and is
// deleted by the retention worker.
func (s *Server) handleE2ESession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	if s.environment == "production" {
		writeError(w, http.StatusForbidden, "e2e sessions are disabled in production", "")
		return
	}
	if s.sessionMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}

	projectID := GetProjectID(r.Context())

	var username string
	if projectID == 0 {
		// Static admin key — no project lookup needed.
		username = "e2e:admin"
	} else {
		project, err := s.repo.GetProjectByID(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load project", "")
			return
		}
		if !project.E2EEnabled {
			writeError(w, http.StatusForbidden, "e2e mode is not enabled for this project", "")
			return
		}
		username = fmt.Sprintf("e2e:%s", project.Slug)
	}

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

	secure := isSecureRequest(r)
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
		"username":           username,
		"account_expires_at": expiresAt.Format(time.RFC3339),
		"session_expires_at": sessionExpires.Format(time.RFC3339),
	})
}
