package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

const requestIDKey contextKey = "request_id"

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var buf [8]byte
			_, _ = rand.Read(buf[:])
			id = hex.EncodeToString(buf[:])
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func getRequestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// loggingMiddleware logs every request with method, path, status, duration, and
// request ID, and (when a recorder is set) feeds request rate + latency to
// self-metrics.
func loggingMiddleware(logger *slog.Logger, rec *selfmetrics.Recorder, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		elapsed := time.Since(start)
		rec.RecordRequest(statusClass(sw.status), float64(elapsed.Microseconds())/1000.0)
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", elapsed.Milliseconds(),
		}
		if id := getRequestID(r); id != "" {
			attrs = append(attrs, "request_id", id)
		}
		logger.Info("http", attrs...)
	})
}

// statusClass buckets an HTTP status into "2xx".."5xx" to keep metric
// cardinality low.
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// recoveryMiddleware catches panics and returns a 500 response.
func recoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				logger.Error("panic recovered",
					"error", fmt.Sprint(rec),
					"stack", string(stack),
					"method", r.Method,
					"path", r.URL.Path,
				)
				writeError(w, http.StatusInternalServerError, "internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware applies CORS headers.
//
// Three tiers, matched on the exact path so a public prefix can never bleed into
// a sibling (e.g. the ingest endpoint /api/v1/spans vs the session-authed SSE
// stream /api/v1/spans/live):
//   - Public, API-key ingest: any origin, but never with credentials.
//   - Session-authed browser ingest: credentials, but only for allow-listed
//     origins — reflecting an arbitrary origin with credentials would let any
//     site drive a logged-in operator's browser into writing data.
//   - Everything else (dashboard APIs, live-tail SSE): the strict AllowedOrigins
//     policy, without credentials.
func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		// Public, API-key-authenticated ingest. Browser SDKs post cross-origin
		// and authenticate with the X-SpanBarn-Api-Key header, not cookies, so
		// any origin is allowed WITHOUT credentials. Never combine a reflected
		// origin with Allow-Credentials here.
		case "/api/v1/spans", "/v1/traces":
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-SpanBarn-Api-Key, Authorization, traceparent, tracestate")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return

		// Session-authenticated, browser-driven ingest (frontend telemetry and
		// client errors). These carry the session cookie, so credentials are
		// required — but only for allow-listed origins. Reflecting an arbitrary
		// origin with credentials was the cross-origin write-injection vector.
		case "/api/v1/telemetry", "/api/v1/client-errors":
			if origin := r.Header.Get("Origin"); origin != "" && originAllowed(origin, allowedOrigins) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-SpanBarn-Api-Key, Authorization, traceparent, tracestate")
				w.Header().Set("Access-Control-Max-Age", "86400")
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// All other routes: respect AllowedOrigins config, without credentials.
		origin := r.Header.Get("Origin")
		if origin != "" && originAllowed(origin, allowedOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// maxBodyBytesMiddleware wraps the request body with http.MaxBytesReader.
func maxBodyBytesMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// apiKeyAuth validates the X-SpanBarn-Api-Key header.
func apiKeyAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-SpanBarn-Api-Key")
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key", "set X-SpanBarn-Api-Key header")
			return
		}
		if key != apiKey {
			writeError(w, http.StatusUnauthorized, "invalid API key", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiKeyOrBearerAuth validates X-SpanBarn-Api-Key or Authorization: Bearer header.
// This supports both the native SpanBarn header and the common OTel Bearer pattern.
func apiKeyOrBearerAuth(apiKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-SpanBarn-Api-Key")
		if key == "" {
			// Fall back to Authorization: Bearer.
			auth := r.Header.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				key = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key", "set X-SpanBarn-Api-Key or Authorization: Bearer header")
			return
		}
		if key != apiKey {
			writeError(w, http.StatusUnauthorized, "invalid API key", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorizerOrBearerAuth validates X-SpanBarn-Api-Key or Authorization: Bearer
// against the Authorizer (static + DB keys).
func authorizerOrBearerAuth(a *auth.Authorizer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-SpanBarn-Api-Key")
		if key == "" {
			ah := r.Header.Get("Authorization")
			if strings.HasPrefix(ah, "Bearer ") {
				key = strings.TrimPrefix(ah, "Bearer ")
			}
		}
		if key == "" {
			writeError(w, http.StatusUnauthorized, "missing API key", "set X-SpanBarn-Api-Key or Authorization: Bearer header")
			return
		}
		projectID, scope, err := a.Authorize(key)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid API key", "")
			return
		}
		ctx := SetProjectID(r.Context(), projectID)
		ctx = SetScope(ctx, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// cacheMiddleware sets Cache-Control on successful GET responses.
func cacheMiddleware(maxAge int, next http.Handler) http.Handler {
	val := fmt.Sprintf("private, max-age=%d", maxAge)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Cache-Control", val)
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sw *statusWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}
