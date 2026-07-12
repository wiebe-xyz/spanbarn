package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Username string `json:"username"`
}

// HandleLogin returns an http.HandlerFunc for POST /api/v1/login.
// A successful login creates a server-side session row (auth_method "local")
// and hands the browser only the opaque handle. onLoginSuccess is called (in
// the request goroutine) after a session is created; pass nil to skip. The
// function should return quickly or launch its own goroutine — the HTTP
// response is written immediately after the call.
//
// accountLimiter (may be nil) throttles attempts per-username under the "login"
// category, so a distributed brute-force spread across many source IPs is still
// bounded per account — the per-IP RateLimitMiddleware only limits a single IP.
func HandleLogin(userAuth *auth.UserAuthenticator, sessions *SessionService, accountLimiter *RateLimiter, onLoginSuccess func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, span := apiTracer.Start(r.Context(), "api.login")
		defer span.End()

		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
			return
		}

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
			return
		}

		if accountLimiter != nil && req.Username != "" {
			if !accountLimiter.Allow("login", "acct:"+req.Username) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "too many login attempts", "")
				return
			}
		}

		if err := userAuth.Authenticate(req.Username, req.Password); err != nil {
			writeError(w, http.StatusUnauthorized, "invalid credentials", "")
			return
		}

		if sessions == nil {
			writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
			return
		}
		token, expires, err := sessions.Create(req.Username, "local", nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create session", "")
			return
		}

		if onLoginSuccess != nil {
			onLoginSuccess()
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			Expires:  expires,
			HttpOnly: true,
			Secure:   isSecureRequest(r),
			SameSite: http.SameSiteLaxMode,
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(loginResponse{Username: req.Username})
	}
}

// HandleLogout returns an http.HandlerFunc for POST /api/v1/logout. It
// destroys the server-side session row and, for OIDC sessions, best-effort
// revokes the stored refresh token at the issuer and returns a logout_url —
// the issuer's end-session URL with id_token_hint — that the SPA should
// navigate to so the IamBarn session dies too (never a front-end-only
// logout).
func HandleLogout(sessions *SessionService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
			return
		}

		var logoutURL string
		if sessions != nil {
			if token := sessionToken(r); token != "" {
				// Bound the best-effort revocation call: logout must respond
				// even when the issuer hangs.
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				logoutURL = sessions.Logout(ctx, token)
				cancel()
			}
		}

		secure := isSecureRequest(r)
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
		// Clear the auth-method hint set by the OIDC callback so a
		// subsequent local login does not inherit the OIDC marker.
		http.SetCookie(w, &http.Cookie{
			Name:     "spanbarn_auth_method",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			SameSite: http.SameSiteLaxMode,
		})
		// Clear raw-token cookies left behind by pre-session-store deploys.
		clearLegacyIAMCookies(w, secure)

		resp := map[string]string{"status": "ok"}
		if logoutURL != "" {
			resp["logout_url"] = logoutURL
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
