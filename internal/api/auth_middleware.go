package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type contextKey string

const (
	ctxProjectID  contextKey = "projectID"
	ctxScope      contextKey = "scope"
	ctxUsername   contextKey = "username"
	ctxWebSession contextKey = "webSession"
)

// SetProjectID stores a project ID in the request context.
func SetProjectID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, ctxProjectID, id)
}

// GetProjectID retrieves the project ID from the request context.
func GetProjectID(ctx context.Context) int64 {
	if v, ok := ctx.Value(ctxProjectID).(int64); ok {
		return v
	}
	return 0
}

// SetScope stores the API key scope in the request context.
func SetScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, ctxScope, scope)
}

// GetScope retrieves the API key scope from the request context.
func GetScope(ctx context.Context) string {
	if v, ok := ctx.Value(ctxScope).(string); ok {
		return v
	}
	return ""
}

// SetUsername stores the authenticated username in the request context.
func SetUsername(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, ctxUsername, name)
}

// GetUsername retrieves the authenticated username from the request context.
func GetUsername(ctx context.Context) string {
	if v, ok := ctx.Value(ctxUsername).(string); ok {
		return v
	}
	return ""
}

// SetWebSession stores the authenticated web session row in the request
// context (session-cookie auth only; API-key and JWT paths have no row).
func SetWebSession(ctx context.Context, ws repository.WebSession) context.Context {
	return context.WithValue(ctx, ctxWebSession, ws)
}

// GetWebSession retrieves the web session row from the request context.
func GetWebSession(ctx context.Context) (repository.WebSession, bool) {
	ws, ok := ctx.Value(ctxWebSession).(repository.WebSession)
	return ws, ok
}

// APIKeyMiddleware validates the X-SpanBarn-Api-Key header and sets the
// project ID and scope in the request context.
func APIKeyMiddleware(authorizer *auth.Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-SpanBarn-Api-Key")
			if key == "" {
				writeError(w, http.StatusUnauthorized, "missing API key", "")
				return
			}

			projectID, scope, err := authorizer.Authorize(key)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid API key", "")
				return
			}

			ctx := SetProjectID(r.Context(), projectID)
			ctx = SetScope(ctx, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionOrReadKey authorizes read/query endpoints with any of:
//   - an API key with the "read" or "full" scope (X-SpanBarn-Api-Key), or
//   - an IamBarn access-token JWT (Authorization: Bearer <jwt>) when OIDC is
//     configured, validated as a resource server and authorized by role/group, or
//   - a SpanBarn session (cookie or Authorization: Bearer <opaque handle>).
//
// When an API key is presented, the request is scoped to that key's project by
// overwriting the project_id query parameter. Ingest-scoped keys are rejected
// with 403. oidcFn is resolved at request time because the OIDC client is wired
// after routes are registered; it may return nil. When authorizer is nil,
// behaviour falls back to JWT/session.
func SessionOrReadKey(sessions *SessionService, authorizer *auth.Authorizer, oidcFn func() *auth.OIDCClient) func(http.Handler) http.Handler {
	sessionMW := SessionMiddleware(sessions)
	return func(next http.Handler) http.Handler {
		session := sessionMW(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key := r.Header.Get("X-SpanBarn-Api-Key"); key != "" && authorizer != nil {
				serveAPIKey(w, r, next, authorizer, key)
				return
			}

			// IamBarn access-token JWT (Bearer with two dots → header.payload.sig).
			// A SpanBarn session token has a single dot, so this is unambiguous.
			if oidcFn != nil {
				if oc := oidcFn(); oc != nil {
					if bearer := bearerToken(r); strings.Count(bearer, ".") == 2 {
						claims, err := oc.VerifyAccessToken(r.Context(), bearer)
						if err != nil {
							writeError(w, http.StatusUnauthorized, "invalid or expired token", "")
							return
						}
						if !oc.Allowed(claims) {
							writeError(w, http.StatusForbidden, "not authorized for SpanBarn", "")
							return
						}
						// OIDC access-token users are read-only, exactly like
						// read-scoped API keys (serveAPIKey below). Without this
						// gate a token authorized only for the read group could
						// reach the mutating project routes mounted on this same
						// middleware (DELETE/approve/e2e/verbose).
						if r.Method != http.MethodGet && r.Method != http.MethodHead {
							writeError(w, http.StatusForbidden, "token is read-only", "")
							return
						}
						ctx := SetUsername(r.Context(), claims.PreferredName())
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			session.ServeHTTP(w, r)
		})
	}
}

// serveAPIKey handles read/full API-key authentication and project scoping.
func serveAPIKey(w http.ResponseWriter, r *http.Request, next http.Handler, authorizer *auth.Authorizer, key string) {
	projectID, scope, err := authorizer.Authorize(key)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid API key", "")
		return
	}
	if scope != "read" && scope != "full" {
		writeError(w, http.StatusForbidden, "API key lacks read scope", "")
		return
	}
	// API keys are read-only: never allow mutating requests, even on handlers
	// that also serve writes for session users.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusForbidden, "API key is read-only", "")
		return
	}

	// Scope the request to the key's project. A non-zero project ID (a
	// per-project key) overrides any client-supplied project_id so a key cannot
	// read another project's data. The static/full key has project ID 0 (all
	// projects); leave the query param untouched so it can target a project.
	ctx := SetScope(r.Context(), scope)
	if projectID != 0 {
		ctx = SetProjectID(ctx, projectID)
		q := r.URL.Query()
		q.Set("project_id", strconv.FormatInt(projectID, 10))
		r.URL.RawQuery = q.Encode()
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

// bearerToken returns the value of an Authorization: Bearer header, or "".
func bearerToken(r *http.Request) string {
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	return ""
}

// SessionMiddleware validates a session cookie or Authorization: Bearer token
// against the web_sessions table and sets the username + session row in the
// request context. For OIDC sessions past their access-token validity it runs
// the refresh_token grant first (singleflighted per session), so a request
// only proceeds while the IamBarn session is still alive — central revocation
// bites at the next refresh, worst-case one access-token lifetime (~15m).
func SessionMiddleware(sessions *SessionService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := sessionToken(r)
			if token == "" {
				writeError(w, http.StatusUnauthorized, "missing session", "")
				return
			}
			if sessions == nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired session", "")
				return
			}

			res, err := sessions.Authenticate(r.Context(), token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired session", "")
				return
			}
			if res.RefreshDue {
				// Served stale from a read-only replica: ask the SPA to POST
				// /api/v1/session/refresh (method-routed to the writer).
				w.Header().Set("X-Session-Refresh-Due", "1")
			}

			ctx := SetUsername(r.Context(), res.Session.Username)
			ctx = SetWebSession(ctx, res.Session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sessionToken extracts the opaque session handle from the session cookie or
// the Authorization: Bearer header.
func sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie("session"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return bearerToken(r)
}
