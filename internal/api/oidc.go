package api

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
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

	// legacy iambarn token cookie names from the pre-session-store design
	// (raw access/refresh tokens in the browser). Never written anymore;
	// logout paths still clear them so upgraded browsers don't keep a
	// long-lived raw refresh token around.
	legacyIAMAccessCookie  = "spanbarn_iam_token"
	legacyIAMRefreshCookie = "spanbarn_iam_refresh"
	legacyIAMProfileCookie = "spanbarn_iam_profile"
)

// clearLegacyIAMCookies removes the retired raw-token/profile cookies left by
// deployments predating token-bound server-side sessions.
func clearLegacyIAMCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{legacyIAMAccessCookie, legacyIAMRefreshCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
	http.SetCookie(w, &http.Cookie{
		Name:     legacyIAMProfileCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// handleOIDCLogin starts the OIDC authorization-code flow by redirecting the
// browser to the iambarn authorize endpoint. State + PKCE verifier share one
// short-lived HttpOnly cookie ("state|verifier"); the nonce gets its own.
// All three are checked on the callback.
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}
	state := randomOIDCToken()
	nonce := randomOIDCToken()
	verifier := oauth2.GenerateVerifier()
	authURL, err := s.oidc.AuthorizeURL(state, nonce, verifier)
	if err != nil {
		s.logger.Warn("oidc: build authorize url", "error", err)
		writeError(w, http.StatusServiceUnavailable, "oidc unavailable", "")
		return
	}
	secure := isSecureRequest(r)
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, state+"|"+verifier, secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, nonce, secure))
	// Stash the post-login redirect target so the callback can send the user
	// back to the page they came from (e.g. /profile after a token refresh).
	// Only local paths are stored, never an attacker-supplied absolute URL.
	if next := safeNextPath(r.URL.Query().Get("next")); next != "/" {
		http.SetCookie(w, oidcShortLivedCookie(oidcNextCookie, next, secure))
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// oidcCallbackInput is the validated parameter set of a callback request.
type oidcCallbackInput struct {
	code     string
	nonce    string
	verifier string
}

// parseOIDCCallback validates state (CSRF), the PKCE verifier segment, the
// nonce cookie and the authorization code. On failure it writes the error
// response and returns ok=false.
func (s *Server) parseOIDCCallback(w http.ResponseWriter, r *http.Request) (oidcCallbackInput, bool) {
	stateCookie, err := r.Cookie(oidcStateCookie)
	state, verifier, hasVerifier := strings.Cut(stateCookieValue(stateCookie, err), "|")
	if state == "" || state != r.URL.Query().Get("state") || !hasVerifier || verifier == "" {
		s.logger.Warn("oidc: state mismatch")
		writeError(w, http.StatusBadRequest, "oidc state mismatch", "")
		return oidcCallbackInput{}, false
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonceCookie.Value == "" {
		writeError(w, http.StatusBadRequest, "oidc nonce missing", "")
		return oidcCallbackInput{}, false
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "oidc code missing", "")
		return oidcCallbackInput{}, false
	}
	return oidcCallbackInput{code: code, nonce: nonceCookie.Value, verifier: verifier}, true
}

// oidcUsername picks the session username from the ID-token claims: prefer
// the display name over the email so the profile chip shows "Wiebe" rather
// than "wiebe@wiebe.xyz".
func oidcUsername(claims auth.OIDCClaims) string {
	if claims.Name != "" {
		return claims.Name
	}
	if name := claims.PreferredName(); name != "" {
		return name
	}
	return "oidc-user"
}

// handleOIDCCallback handles the redirect back from iambarn. On success it
// persists a server-side session row carrying the IamBarn token set (access,
// refresh, id_token), the IdP session id (sid) and a claims snapshot, then
// issues only the opaque `session` cookie — no token ever reaches the browser.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}
	in, ok := s.parseOIDCCallback(w, r)
	if !ok {
		return
	}
	exchanged, err := s.oidc.ExchangeFull(r.Context(), in.code, in.nonce, in.verifier)
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
	token, expires, err := s.sessions.Create(oidcUsername(claims), "oidc", &OIDCSessionData{
		Claims:          claims,
		IDToken:         exchanged.IDToken,
		AccessToken:     exchanged.AccessToken,
		RefreshToken:    exchanged.RefreshToken,
		AccessExpiresAt: exchanged.ExpiresAt,
	})
	if err != nil {
		s.logger.Error("oidc: create session row", "error", err)
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}
	secure := isSecureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
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
	// Clear the short-lived state/nonce/next cookies and any raw-token
	// cookies left behind by the previous cookie-based design.
	http.SetCookie(w, oidcShortLivedCookie(oidcStateCookie, "", secure))
	http.SetCookie(w, oidcShortLivedCookie(oidcNonceCookie, "", secure))
	clearLegacyIAMCookies(w, secure)
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

// stateCookieValue unwraps the state cookie lookup, returning "" for any
// missing/unreadable cookie so the caller has a single validation branch.
func stateCookieValue(c *http.Cookie, err error) string {
	if err != nil || c == nil {
		return ""
	}
	return c.Value
}

// handleOIDCLogoutComplete is the landing endpoint IamBarn redirects the
// browser back to after an RP-initiated /oauth2/end-session logout (it is the
// registered post_logout_redirect_uri). By the time we get here the IamBarn
// session is already ended; this destroys SpanBarn's own session row, clears
// the cookies and sends the user to the login page. It is intentionally
// public — the whole point is to run while tearing a session down.
func (s *Server) handleOIDCLogoutComplete(w http.ResponseWriter, r *http.Request) {
	secure := isSecureRequest(r)
	// The row may already be gone (server-driven logout deletes before
	// redirecting here); deleting is idempotent either way.
	if s.sessions != nil {
		if token := sessionToken(r); token != "" {
			_ = s.sessions.Logout(r.Context(), token)
		}
	}
	clearLegacyIAMCookies(w, secure)
	jsReadable := map[string]bool{"spanbarn_auth_method": true}
	for _, name := range []string{"session", "spanbarn_auth_method"} {
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
	// Land with ?logged_out=1 so the login page shows a signed-out state instead
	// of auto-restarting OIDC — otherwise logout bounces straight back into the
	// IdP's authorize/login and the user never sees they were signed out.
	http.Redirect(w, r, "/login?logged_out=1", http.StatusFound)
}

// handleSessionRefresh (POST /api/v1/session/refresh) forces the refresh
// grant for the caller's session. It exists for split deployments: reader
// pods mount SQLite read-only and serve stale sessions with an
// X-Session-Refresh-Due header; the SPA then POSTs here, and the ingress
// method rule routes every POST to the writer, which can persist the rotated
// tokens. On single-process deployments it is a harmless no-op (the
// middleware already refreshed inline).
func (s *Server) handleSessionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "session unavailable", "")
		return
	}
	token := sessionToken(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "missing session", "")
		return
	}
	ws, err := s.sessions.RefreshNow(r.Context(), token, "")
	if err != nil {
		if errors.Is(err, errRefreshUnavailable) {
			// Read-only replica or transient IdP failure: nothing persisted,
			// session still (stale-)servable. 503 signals "try again".
			writeError(w, http.StatusServiceUnavailable, "refresh unavailable", "")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid or expired session", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"access_expires_at": ws.AccessExpiresAt,
	})
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
