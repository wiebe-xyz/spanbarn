package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// safeNextPath returns next only if it is a root-relative local path, otherwise
// "/". This blocks open-redirect abuse of the post-login ?next= parameter: an
// absolute URL, a protocol-relative "//evil.com", or a "/\evil" backslash trick
// would otherwise send the freshly-authenticated user to an attacker's site.
func safeNextPath(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

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
	secure := isSecureRequest(r)
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, state, secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, nonce, secure))
	// Stash the post-login redirect target so the callback can send the user
	// back to the page they came from (e.g. /profile after a token refresh).
	// Only local paths are stored, never an attacker-supplied absolute URL.
	if next := safeNextPath(r.URL.Query().Get("next")); next != "/" {
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
	secure := isSecureRequest(r)
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
	// Non-HttpOnly profile snapshot (name/email/picture) taken from the ID token
	// so the header chip can render the live avatar + name with no runtime
	// dependency on IamBarn (the access token is short-lived; this is stable for
	// the session). Values are non-secret identity attributes the user owns.
	profile, _ := json.Marshal(map[string]string{
		"name":    username,
		"email":   claims.Email,
		"picture": claims.Picture,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "spanbarn_iam_profile",
		Value:    base64.RawURLEncoding.EncodeToString(profile),
		Path:     "/",
		Expires:  expires,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Clear the short-lived state/nonce/next cookies.
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, "", secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, "", secure))
	// Redirect back to the original page if one was stashed, otherwise /.
	// Re-validate at the point of use so only a local path is ever followed.
	next := "/"
	if c, err := r.Cookie(oidcNextCookie); err == nil {
		next = safeNextPath(c.Value)
	}
	http.SetCookie(w, oidcShortLivedCookie(oidcNextCookie, "", secure))
	// Prevent the browser from caching this redirect — a cached response
	// would replay the already-consumed authorization code on reload.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusFound)
}

// handleOIDCLogoutComplete is the landing endpoint IamBarn redirects the
// browser back to after an RP-initiated /oauth2/end-session logout (it is the
// registered post_logout_redirect_uri). By the time we get here the IamBarn
// session is already ended; this clears SpanBarn's own session cookies and
// sends the user to the login page. It is intentionally public — the whole
// point is to run while tearing a session down.
func (s *Server) handleOIDCLogoutComplete(w http.ResponseWriter, r *http.Request) {
	secure := isSecureRequest(r)
	jsReadable := map[string]bool{"spanbarn_auth_method": true, "spanbarn_iam_profile": true}
	for _, name := range []string{"session", "spanbarn_auth_method", "spanbarn_iam_token", "spanbarn_iam_profile"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: !jsReadable[name],
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Never cache the teardown; a replayed 302 must not resurrect a cleared UI.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login", http.StatusFound)
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
