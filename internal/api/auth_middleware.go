package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

type contextKey string

const (
	ctxProjectID contextKey = "projectID"
	ctxScope     contextKey = "scope"
	ctxUsername   contextKey = "username"
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
