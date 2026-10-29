package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// rejectedAuthInterval is how often one key fingerprint is logged. Rejections
// in between are counted and reported on the next line for that fingerprint.
const rejectedAuthInterval = time.Minute

// rejectedAuthMaxKeys bounds the fingerprints remembered between lines, so a
// caller cycling through random keys cannot grow the map without limit.
const rejectedAuthMaxKeys = 1024

// ctxAuthPassed marks, per request, that the auth wrapper let it through.
const ctxAuthPassed contextKey = "authPassed"

// rejectedAuthLog logs ingest requests whose API key was refused, so a sender
// with a stale key can be found from the logs. It logs the first 8 hex digits
// of the key's sha256, never the key, with the user-agent, path, Origin and
// the Referer's scheme and host, at INFO: a refused key is the caller's
// problem and must not file a BugBarn issue.
type rejectedAuthLog struct {
	logger *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*rejectedAuthEntry
}

type rejectedAuthEntry struct {
	logged     time.Time
	suppressed int
}

func newRejectedAuthLog(logger *slog.Logger) *rejectedAuthLog {
	return &rejectedAuthLog{logger: logger, now: time.Now, entries: make(map[string]*rejectedAuthEntry)}
}

// keyFingerprint is the first 8 hex digits of the key's sha256, or "none"
// when the request carried no key.
func keyFingerprint(key string) string {
	if key == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

// rejectedRequest is what a refusal line says about the caller besides its
// key. Origin and Referer name the page when the sender is a browser.
type rejectedRequest struct {
	userAgent string
	path      string
	origin    string
	referer   string
}

// rejectedHeaderMax bounds each caller-supplied header in the log line.
const rejectedHeaderMax = 256

func newRejectedRequest(userAgent, path, origin, referer string) rejectedRequest {
	return rejectedRequest{
		userAgent: truncate(userAgent, rejectedHeaderMax),
		path:      path,
		origin:    truncate(origin, rejectedHeaderMax),
		referer:   truncate(schemeAndHost(referer), rejectedHeaderMax),
	}
}

// schemeAndHost reduces a Referer to scheme://host, so a page's path and
// query string, which can carry tokens, never reach the log.
func schemeAndHost(ref string) string {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// reject records one refused request and logs it unless the same fingerprint
// was logged within rejectedAuthInterval. A nil log does nothing.
func (l *rejectedAuthLog) reject(key string, req rejectedRequest) {
	if l == nil {
		return
	}
	fp := keyFingerprint(key)
	now := l.now()
	l.mu.Lock()
	e := l.entries[fp]
	if e != nil && now.Sub(e.logged) < rejectedAuthInterval {
		e.suppressed++
		l.mu.Unlock()
		return
	}
	suppressed := 0
	if e != nil {
		suppressed = e.suppressed
	}
	if e == nil && len(l.entries) >= rejectedAuthMaxKeys {
		l.entries = make(map[string]*rejectedAuthEntry)
	}
	l.entries[fp] = &rejectedAuthEntry{logged: now}
	l.mu.Unlock()
	l.logger.Info("ingest auth rejected",
		"key_sha256_prefix", fp,
		"user_agent", req.userAgent,
		"path", req.path,
		"origin", req.origin,
		"referer", req.referer,
		"suppressed", suppressed,
	)
}

// wrap returns auth with every refusal logged. auth either answers the request
// itself (a refusal) or calls the handler it wraps; a request that never
// reaches that handler is the refusal.
func (l *rejectedAuthLog) wrap(auth func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if l == nil {
		return auth
	}
	return func(next http.Handler) http.Handler {
		guarded := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if passed, ok := r.Context().Value(ctxAuthPassed).(*bool); ok {
				*passed = true
			}
			next.ServeHTTP(w, r)
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			passed := false
			guarded.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxAuthPassed, &passed)))
			if !passed {
				l.reject(requestKey(r), newRejectedRequest(r.UserAgent(), r.URL.Path, r.Header.Get("Origin"), r.Referer()))
			}
		})
	}
}

// requestKey reads the key the ingest auth wrappers read: X-SpanBarn-Api-Key,
// else Authorization: Bearer.
func requestKey(r *http.Request) string {
	if key := r.Header.Get("X-SpanBarn-Api-Key"); key != "" {
		return key
	}
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	return ""
}
