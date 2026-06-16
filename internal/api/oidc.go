package api

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"time"
)

const (
	oidcStateCookie = "spanbarn_oidc_state"
	oidcNonceCookie = "spanbarn_oidc_nonce"
	oidcNextCookie  = "spanbarn_oidc_next"
	oidcCookieTTL   = 10 * time.Minute
)

// handleOIDCLogin starts the OIDC authorization-code flow by redirecting the
// browser to the iambarn authorize endpoint. State + nonce are stored in
// short-lived, HttpOnly cookies and checked on the callback.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}
	state := randomOIDCToken()
	nonce := randomOIDCToken()
	authURL, err := s.oidc.AuthorizeURL(state, nonce)
	if err != nil {
		s.logger.Warn("oidc: build authorize url", "error", err)
		writeError(w, http.StatusServiceUnavailable, "oidc unavailable", "")
		return
	}
	secure := r.TLS != nil
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, state, secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, nonce, secure))
	// Stash the post-login redirect target so the callback can send the user
	// back to the page they came from (e.g. /profile after a token refresh).
	if next := r.URL.Query().Get("next"); next != "" {
		http.SetCookie(w, oidcShortLivedCookie(oidcNextCookie, next, secure))
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleOIDCCallback handles the redirect back from iambarn. On success it
// issues a local session cookie that authenticates the browser for the SPA.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}
	if s.sessionMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}
	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		s.logger.Warn("oidc: state mismatch")
		writeError(w, http.StatusBadRequest, "oidc state mismatch", "")
		return
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonceCookie.Value == "" {
		writeError(w, http.StatusBadRequest, "oidc nonce missing", "")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "oidc code missing", "")
		return
	}
	exchanged, err := s.oidc.ExchangeFull(r.Context(), code, nonceCookie.Value)
	if err != nil {
		s.logger.Warn("oidc: exchange failed", "error", err)
		writeError(w, http.StatusUnauthorized, "oidc exchange failed", "")
		return
	}
	claims := exchanged.Claims
	if !s.oidc.Allowed(claims) {
		s.logger.Warn("oidc: access denied",
			"sub", claims.Subject, "groups", claims.Groups, "roles", claims.Roles)
		writeError(w, http.StatusForbidden, "access denied: user is not a member of the required group", "")
		return
	}
	// Prefer the display name over the email as the session username so the
	// profile chip shows "Wiebe" rather than "wiebe@wiebe.xyz".
	username := claims.Name
	if username == "" {
		username = claims.PreferredName()
	}
	if username == "" {
		username = "oidc-user"
	}
	token, err := s.sessionMgr.Create(username)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}
	secure := r.TLS != nil
	expires := time.Now().Add(s.sessionMgr.TTL())
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Store the OIDC access token so SpanBarn can proxy iambarn-profile widget
	// API calls server-to-server. HttpOnly prevents JS access; the proxy
	// handler reads it for outbound Bearer requests to IamBarn.
	if exchanged.AccessToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "spanbarn_iam_token",
			Value:    exchanged.AccessToken,
			Path:     "/",
			Expires:  expires,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Non-HttpOnly hint so the SPA can show OIDC-specific UI only for
	// sessions that actually came from iambarn. Same expiry as the session.
	http.SetCookie(w, &http.Cookie{
		Name:     "spanbarn_auth_method",
		Value:    "oidc",
		Path:     "/",
		Expires:  expires,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Clear the short-lived state/nonce/next cookies.
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, "", secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, "", secure))
	// Redirect back to the original page if one was stashed, otherwise /.
	next := "/"
	if c, err := r.Cookie(oidcNextCookie); err == nil && c.Value != "" {
		next = c.Value
	}
	http.SetCookie(w, oidcShortLivedCookie(oidcNextCookie, "", secure))
	// Prevent the browser from caching this redirect — a cached response
	// would replay the already-consumed authorization code on reload.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusFound)
}

func oidcShortLivedCookie(name, value string, secure bool) *http.Cookie {
	maxAge := int(oidcCookieTTL.Seconds())
	if value == "" {
		maxAge = -1
	}
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
}

func randomOIDCToken() string {
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}
