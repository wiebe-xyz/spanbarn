package api

import (
	"net/http"
)

// handleBackchannelLogout implements the RP side of OIDC Back-Channel Logout
// 1.0 (POST /api/v1/oidc/backchannel-logout). IamBarn calls it
// server-to-server with a signed logout token when a session/user is revoked
// centrally, so revocation reaches SpanBarn in seconds instead of waiting for
// the next refresh.
//
// The route is public (no session — the IdP is the caller) but rate-limited;
// authenticity comes exclusively from the logout token's signature, issuer,
// audience and claim requirements, verified against the IdP's JWKS. Per spec:
// 200 on success, 400 on any validation failure, and Cache-Control: no-store.
func (s *Server) handleBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}
	if s.oidc == nil || s.sessions == nil {
		writeError(w, http.StatusNotFound, "oidc not configured", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid form body", "")
		return
	}
	raw := r.PostFormValue("logout_token")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "missing logout_token", "")
		return
	}

	claims, err := s.oidc.VerifyLogoutToken(r.Context(), raw)
	if err != nil {
		s.logger.Warn("backchannel-logout: token rejected", "error", err)
		writeError(w, http.StatusBadRequest, "invalid logout token", "")
		return
	}

	// Prefer the precise kill (one IdP session) over the broad one (every
	// session of the subject).
	var n int64
	var target string
	if claims.SessionID != "" {
		n, err = s.sessions.RevokeByIdpSid(claims.SessionID)
		target = "sid"
	} else {
		n, err = s.sessions.RevokeByIdpSub(claims.Subject)
		target = "sub"
	}
	if err != nil {
		s.logger.Error("backchannel-logout: session deletion failed", "error", err)
		writeError(w, http.StatusBadRequest, "could not revoke sessions", "")
		return
	}
	s.logger.Info("backchannel-logout: sessions revoked",
		"by", target, "sub", claims.Subject, "sid", claims.SessionID, "count", n)
	// 200 with an empty body per OIDC Back-Channel Logout 1.0 §2.8. A logout
	// token for sessions we no longer hold (n == 0) is still a success.
	w.WriteHeader(http.StatusOK)
}
