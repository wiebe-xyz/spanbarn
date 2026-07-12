package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// WebSessionStore is the persistence surface SessionService needs. It is
// implemented by *repository.Repository (repo_sessions.go).
type WebSessionStore interface {
	CreateWebSession(ws repository.WebSession) error
	GetWebSessionByIDHash(idHash string) (repository.WebSession, error)
	UpdateWebSessionTokens(idHash, idToken, accessToken, refreshToken string, accessExpiresAt int64, claimsJSON string, lastRefreshAt int64) error
	MarkWebSessionRefreshFailing(idHash string, since int64) error
	DeleteWebSession(idHash string) error
	DeleteWebSessionsByIdpSid(sid string) (int64, error)
	DeleteWebSessionsByIdpSub(sub string) (int64, error)
	ReadOnly() bool
}

// errSessionInvalid is the single unauthenticated outcome: unknown handle,
// absolute expiry, dead refresh token, or grace exhausted. Callers answer 401
// without distinguishing — the fix is always a fresh login.
var errSessionInvalid = errors.New("session invalid")

// errRefreshUnavailable reports a refresh that could not be performed right
// now (transient IdP failure, or a read-only replica that must not spend the
// single-use refresh token). The session itself may still be servable.
var errRefreshUnavailable = errors.New("session refresh unavailable")

// accessTokenSkew refreshes slightly BEFORE the access token's expiry so an
// outbound call started right at the boundary doesn't race a dying token.
const accessTokenSkew = 30 * time.Second

// accessTokenFallbackTTL is assumed when a token response omits expires_in
// (never happens against iambarn; a zero expiry would otherwise mean
// "refresh on every request").
const accessTokenFallbackTTL = 15 * time.Minute

// OIDCSessionData carries the IdP artifacts persisted onto an OIDC session
// row at login time.
type OIDCSessionData struct {
	Claims          auth.OIDCClaims
	IDToken         string
	AccessToken     string
	RefreshToken    string
	AccessExpiresAt time.Time
}

// AuthResult is a successfully authenticated request's session view.
type AuthResult struct {
	Session repository.WebSession
	// RefreshDue is set when the session was served with a stale access
	// token because this replica cannot refresh (read-only store). The
	// middleware surfaces it as an X-Session-Refresh-Due header so the SPA
	// can trigger POST /api/v1/session/refresh, which the ingress routes to
	// the writer.
	RefreshDue bool
}

// SessionService owns token-bound server-side sessions: minting rows at
// login, validating + refreshing them per request, and destroying them at
// logout/revocation. The browser cookie is an opaque handle; all state —
// including the IamBarn token set for OIDC sessions — lives in web_sessions.
type SessionService struct {
	store  WebSessionStore
	ttl    time.Duration // absolute session cap (SPANBARN_SESSION_TTL_SECONDS)
	grace  time.Duration // stale-serve ceiling on transient refresh failure
	oidc   func() *auth.OIDCClient
	logger *slog.Logger
	now    func() time.Time

	// refresh collapses concurrent refreshes of the same session into one
	// token-endpoint call, keyed by the session's id_hash. Refresh tokens are
	// single-use: two concurrent grants with the same token would be treated
	// as a replay by iambarn and revoke the whole token family.
	refresh singleflight.Group
}

// NewSessionService wires a session service over the given store.
// ttlSeconds <= 0 defaults to 12h, graceSeconds <= 0 to 1h.
func NewSessionService(store WebSessionStore, ttlSeconds, graceSeconds int, logger *slog.Logger) *SessionService {
	ttl := time.Duration(ttlSeconds) * time.Second
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	grace := time.Duration(graceSeconds) * time.Second
	if grace <= 0 {
		grace = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionService{
		store:  store,
		ttl:    ttl,
		grace:  grace,
		oidc:   func() *auth.OIDCClient { return nil },
		logger: logger,
		now:    time.Now,
	}
}

// SetOIDCProvider wires the request-time OIDC client getter (the client is
// attached to the server after construction).
func (s *SessionService) SetOIDCProvider(fn func() *auth.OIDCClient) {
	if fn != nil {
		s.oidc = fn
	}
}

// TTL returns the absolute session cap.
func (s *SessionService) TTL() time.Duration { return s.ttl }

// Create mints an opaque session token and persists its row. method is one of
// "oidc", "local", "e2e"; oidcData must be non-nil exactly for "oidc".
func (s *SessionService) Create(username, method string, oidcData *OIDCSessionData) (token string, expires time.Time, err error) {
	token = auth.NewSessionToken()
	now := s.now()
	expires = now.Add(s.ttl)
	ws := repository.WebSession{
		IDHash:            auth.HashSessionToken(token),
		Username:          username,
		AuthMethod:        method,
		CreatedAt:         now.Unix(),
		AbsoluteExpiresAt: expires.Unix(),
	}
	if oidcData != nil {
		claimsJSON, merr := json.Marshal(oidcData.Claims)
		if merr != nil {
			return "", time.Time{}, merr
		}
		accessExp := oidcData.AccessExpiresAt
		if accessExp.IsZero() {
			accessExp = now.Add(accessTokenFallbackTTL)
		}
		ws.IdpSub = oidcData.Claims.Subject
		ws.IdpSid = oidcData.Claims.SessionID
		ws.IDToken = oidcData.IDToken
		ws.AccessToken = oidcData.AccessToken
		ws.RefreshToken = oidcData.RefreshToken
		ws.AccessExpiresAt = accessExp.Unix()
		ws.ClaimsJSON = string(claimsJSON)
	}
	if err := s.store.CreateWebSession(ws); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Authenticate validates an opaque session token: row lookup by hash,
// absolute-cap enforcement, and — for OIDC sessions past their access-token
// validity — the refresh_token grant (grace-bounded on transient failure).
// Returns errSessionInvalid for every unauthenticated outcome.
func (s *SessionService) Authenticate(ctx context.Context, token string) (AuthResult, error) {
	ws, err := s.load(token)
	if err != nil {
		return AuthResult{}, err
	}
	if ws.AuthMethod != "oidc" || !s.accessExpired(ws) {
		return AuthResult{Session: ws}, nil
	}

	if s.store.ReadOnly() {
		return s.serveStaleReadOnly(ws)
	}

	refreshed, err := s.refreshSession(ctx, ws.IDHash, ws.AccessToken)
	if err == nil {
		return AuthResult{Session: refreshed}, nil
	}
	if errors.Is(err, errRefreshUnavailable) {
		// Transient IdP failure: serve stale until the grace ceiling,
		// measured from the FIRST failure.
		failingSince := refreshed.RefreshFailingSince
		if failingSince == 0 {
			failingSince = s.now().Unix()
		}
		if s.now().Unix()-failingSince <= int64(s.grace.Seconds()) {
			return AuthResult{Session: refreshed}, nil
		}
		s.logger.Warn("session: refresh grace exhausted", "user", refreshed.Username)
		return AuthResult{}, errSessionInvalid
	}
	return AuthResult{}, err
}

// load resolves a token to its row and enforces the absolute cap.
func (s *SessionService) load(token string) (repository.WebSession, error) {
	if token == "" || s.store == nil {
		return repository.WebSession{}, errSessionInvalid
	}
	ws, err := s.store.GetWebSessionByIDHash(auth.HashSessionToken(token))
	if err != nil {
		return repository.WebSession{}, errSessionInvalid
	}
	if s.now().Unix() >= ws.AbsoluteExpiresAt {
		if !s.store.ReadOnly() {
			_ = s.store.DeleteWebSession(ws.IDHash)
		}
		return repository.WebSession{}, errSessionInvalid
	}
	return ws, nil
}

// accessExpired reports whether an OIDC session's access token is past (or
// within accessTokenSkew of) its expiry.
func (s *SessionService) accessExpired(ws repository.WebSession) bool {
	if ws.AccessExpiresAt == 0 {
		return false
	}
	return s.now().Add(accessTokenSkew).Unix() >= ws.AccessExpiresAt
}

// serveStaleReadOnly handles an expired OIDC session on a replica that mounts
// the database read-only (reader/ingest pods). Attempting the refresh grant
// here would obtain a rotated refresh token that cannot be persisted —
// burning the single-use token and replay-revoking the family on the next
// writer-side refresh. So the session is served stale within the grace
// window, flagged RefreshDue so the SPA fires the writer-routed refresh.
func (s *SessionService) serveStaleReadOnly(ws repository.WebSession) (AuthResult, error) {
	if s.now().Unix() <= ws.AccessExpiresAt+int64(s.grace.Seconds()) {
		return AuthResult{Session: ws, RefreshDue: true}, nil
	}
	return AuthResult{}, errSessionInvalid
}

// RefreshNow forces a refresh for the given session token regardless of local
// expiry bookkeeping — used by POST /api/v1/session/refresh and by the IAM
// proxy when the upstream rejects a stored access token with 401 before its
// bookkept expiry (central revocation, clock drift).
func (s *SessionService) RefreshNow(ctx context.Context, token, staleAccessToken string) (repository.WebSession, error) {
	ws, err := s.load(token)
	if err != nil {
		return repository.WebSession{}, err
	}
	if ws.AuthMethod != "oidc" {
		return ws, nil
	}
	if s.store.ReadOnly() {
		return ws, errRefreshUnavailable
	}
	if staleAccessToken == "" {
		staleAccessToken = ws.AccessToken
	}
	refreshed, err := s.refreshSession(ctx, ws.IDHash, staleAccessToken)
	if err != nil {
		return refreshed, err
	}
	return refreshed, nil
}

// refreshSession runs the refresh_token grant for one session, singleflighted
// by id_hash. staleAccessToken identifies the caller's view: if the row
// already carries a different access token, another request refreshed in the
// meantime and the fresh row is returned without a grant.
//
// Returns:
//   - (fresh row, nil) on success or when already refreshed elsewhere;
//   - (stale row, errRefreshUnavailable) on transient IdP failure — the row
//     carries refresh_failing_since so the caller can apply grace;
//   - (zero, errSessionInvalid) when the refresh token is dead
//     (invalid_grant) or the session vanished — the row is deleted.
func (s *SessionService) refreshSession(ctx context.Context, idHash, staleAccessToken string) (repository.WebSession, error) {
	v, err, _ := s.refresh.Do(idHash, func() (any, error) {
		cur, gerr := s.store.GetWebSessionByIDHash(idHash)
		if gerr != nil {
			return nil, errSessionInvalid
		}
		if cur.AccessToken != staleAccessToken {
			// Rotated by a concurrent request while we waited on the flight
			// group; nothing to do.
			return cur, nil
		}
		return s.performRefresh(ctx, cur)
	})
	if err != nil {
		if ws, ok := v.(repository.WebSession); ok {
			return ws, err
		}
		return repository.WebSession{}, err
	}
	return v.(repository.WebSession), nil
}

// performRefresh executes one refresh grant and persists the outcome. Runs
// inside the singleflight; never called concurrently for one session.
func (s *SessionService) performRefresh(ctx context.Context, cur repository.WebSession) (any, error) {
	if cur.RefreshToken == "" {
		// Nothing to renew with (offline_access not granted): the session's
		// validity IS the access token's validity.
		_ = s.store.DeleteWebSession(cur.IDHash)
		return nil, errSessionInvalid
	}
	oc := s.oidc()
	if oc == nil {
		// OIDC not configured on this replica — cannot execute the grant.
		return s.markRefreshFailing(cur)
	}

	refreshed, rerr := oc.Refresh(ctx, cur.RefreshToken)
	if rerr != nil {
		if errors.Is(rerr, auth.ErrRefreshInvalid) {
			// Revoked / rotated-and-replayed / user suspended: the session is
			// dead NOW. invalid_grant never gets grace.
			s.logger.Info("session: refresh token invalid, revoking session",
				"user", cur.Username, "sub", cur.IdpSub)
			_ = s.store.DeleteWebSession(cur.IDHash)
			return nil, errSessionInvalid
		}
		s.logger.Warn("session: transient refresh failure", "user", cur.Username, "error", rerr)
		return s.markRefreshFailing(cur)
	}

	accessExp := refreshed.ExpiresAt
	if accessExp.IsZero() {
		accessExp = s.now().Add(accessTokenFallbackTTL)
	}
	var idToken, claimsJSON string
	if refreshed.Claims != nil {
		// Fresh claims snapshot: central group/role changes propagate here.
		// A user dropped from the required group loses the session outright.
		if !oc.Allowed(*refreshed.Claims) {
			s.logger.Info("session: refreshed claims no longer authorized, revoking session",
				"user", cur.Username, "sub", cur.IdpSub)
			_ = s.store.DeleteWebSession(cur.IDHash)
			return nil, errSessionInvalid
		}
		if b, merr := json.Marshal(refreshed.Claims); merr == nil {
			claimsJSON = string(b)
		}
		idToken = refreshed.IDToken
	}
	now := s.now()
	if uerr := s.store.UpdateWebSessionTokens(cur.IDHash, idToken, refreshed.AccessToken,
		refreshed.RefreshToken, accessExp.Unix(), claimsJSON, now.Unix()); uerr != nil {
		// The row vanished mid-refresh (back-channel logout won the race) or
		// the write failed; treat the session as gone rather than serving
		// tokens that were never persisted.
		s.logger.Warn("session: persisting refreshed tokens failed", "error", uerr)
		return nil, errSessionInvalid
	}
	cur.IDToken = firstNonEmpty(idToken, cur.IDToken)
	cur.AccessToken = refreshed.AccessToken
	cur.RefreshToken = refreshed.RefreshToken
	cur.AccessExpiresAt = accessExp.Unix()
	if claimsJSON != "" {
		cur.ClaimsJSON = claimsJSON
	}
	cur.LastRefreshAt = now.Unix()
	cur.RefreshFailingSince = 0
	return cur, nil
}

// markRefreshFailing stamps the first-failure time (if not already set) and
// returns the stale row with errRefreshUnavailable for grace handling.
func (s *SessionService) markRefreshFailing(cur repository.WebSession) (any, error) {
	now := s.now().Unix()
	_ = s.store.MarkWebSessionRefreshFailing(cur.IDHash, now)
	if cur.RefreshFailingSince == 0 {
		cur.RefreshFailingSince = now
	}
	return cur, errRefreshUnavailable
}

// Logout destroys the session behind token: best-effort refresh-token
// revocation at the issuer, row deletion, and — for OIDC sessions — the
// RP-initiated end-session URL the browser should be sent to so the IdP
// session dies too. Unknown/dead tokens are a silent no-op.
func (s *SessionService) Logout(ctx context.Context, token string) (logoutURL string) {
	if token == "" || s.store == nil {
		return ""
	}
	idHash := auth.HashSessionToken(token)
	ws, err := s.store.GetWebSessionByIDHash(idHash)
	if err != nil {
		return ""
	}
	if ws.AuthMethod == "oidc" {
		if oc := s.oidc(); oc != nil {
			if ws.RefreshToken != "" {
				if rerr := oc.RevokeRefreshToken(ctx, ws.RefreshToken); rerr != nil {
					s.logger.Warn("logout: revoke refresh token failed", "error", rerr)
				}
			}
			if u, uerr := oc.EndSessionURL(ws.IDToken); uerr == nil {
				logoutURL = u
			} else {
				s.logger.Warn("logout: build end-session url failed", "error", uerr)
			}
		}
	}
	if derr := s.store.DeleteWebSession(idHash); derr != nil {
		s.logger.Warn("logout: delete session row failed", "error", derr)
	}
	return logoutURL
}

// RevokeByIdpSid deletes every session bound to one IdP session id
// (back-channel logout with sid).
func (s *SessionService) RevokeByIdpSid(sid string) (int64, error) {
	return s.store.DeleteWebSessionsByIdpSid(sid)
}

// RevokeByIdpSub deletes every session of one IdP subject (back-channel
// logout without sid).
func (s *SessionService) RevokeByIdpSub(sub string) (int64, error) {
	return s.store.DeleteWebSessionsByIdpSub(sub)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
