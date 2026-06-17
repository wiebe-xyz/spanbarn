package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

type contextKey string

const (
	ctxProjectID contextKey = "projectID"
	ctxScope     contextKey = "scope"
	ctxUsername  contextKey = "username"
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
//   - a SpanBarn session (cookie or Authorization: Bearer <hmac>).
//
// When an API key is presented, the request is scoped to that key's project by
// overwriting the project_id query parameter. Ingest-scoped keys are rejected
// with 403. oidcFn is resolved at request time because the OIDC client is wired
// after routes are registered; it may return nil. When authorizer is nil,
// behaviour falls back to JWT/session.
func SessionOrReadKey(sm *auth.SessionManager, authorizer *auth.Authorizer, oidcFn func() *auth.OIDCClient) func(http.Handler) http.Handler {
	sessionMW := SessionMiddleware(sm)
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
// and sets the username in the request context.
func SessionMiddleware(sm *auth.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var token string

			// Try cookie first.
			if cookie, err := r.Cookie("session"); err == nil {
				token = cookie.Value
			}

			// Fall back to Authorization: Bearer header.
			if token == "" {
				if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
					token = strings.TrimPrefix(ah, "Bearer ")
				}
			}

			if token == "" {
				writeError(w, http.StatusUnauthorized, "missing session", "")
				return
			}

			username, err := sm.Validate(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired session", "")
				return
			}

			ctx := SetUsername(r.Context(), username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
