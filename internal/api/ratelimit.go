package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// trustProxyHeaders controls whether X-Forwarded-For/X-Real-IP are trusted when
// determining the client IP for rate limiting. It is process-global because the
// trust decision is a property of the deployment, not of any one limiter.
var trustProxyHeaders atomic.Bool

// SetTrustProxy configures proxy-header trust for client-IP determination.
// Enable it only when the app sits behind a reverse proxy that overwrites the
// forwarding headers (Caddy/Nginx); otherwise clients could spoof them to evade
// rate limits. Call once at startup, before serving.
func SetTrustProxy(v bool) { trustProxyHeaders.Store(v) }

// clientIP returns the rate-limit key IP for a request. Behind a trusted proxy
// it uses the right-most X-Forwarded-For entry (the hop the proxy itself
// appended — left entries can be client-forged) or X-Real-IP, falling back to
// the transport peer. Directly exposed, it always uses the transport peer so
// forwarding headers cannot be spoofed to dodge limits.
func clientIP(r *http.Request) string {
	if trustProxyHeaders.Load() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// bucket implements a token bucket for rate limiting.
type bucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

// RateLimiter provides per-IP token bucket rate limiting across categories.
type RateLimiter struct {
	limiters map[string]*bucket // keyed by "category:clientIP"
	configs  map[string]float64 // category -> tokens per second
	mu       sync.Mutex
	nowFunc  func() time.Time // for testing
}

// NewRateLimiter creates a RateLimiter with the given per-minute rates for each category.
func NewRateLimiter(loginRate, ingestRate, apiRate int) *RateLimiter {
	configs := map[string]float64{
		"login":  float64(loginRate) / 60.0,
		"ingest": float64(ingestRate) / 60.0,
		"api":    float64(apiRate) / 60.0,
	}
	rl := &RateLimiter{
		limiters: make(map[string]*bucket),
		configs:  configs,
		nowFunc:  time.Now,
	}
	go rl.cleanup()
	return rl
}

// Allow checks whether a request from clientIP in the given category is allowed.
func (rl *RateLimiter) Allow(category, clientIP string) bool {
	key := fmt.Sprintf("%s:%s", category, clientIP)
	now := rl.nowFunc()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.limiters[key]
	if !ok {
		rate, exists := rl.configs[category]
		if !exists {
			return true // unknown category, allow
		}
		maxTokens := rate * 60 // bucket size = 1 minute worth
		b = &bucket{
			tokens:     maxTokens, // start full
			maxTokens:  maxTokens,
			refillRate: rate,
			lastRefill: now,
		}
		rl.limiters[key] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
		b.lastRefill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// cleanup periodically removes stale entries.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for key, b := range rl.limiters {
			staleThreshold := 2 * b.maxTokens / b.refillRate // 2x refill time in seconds
			if now.Sub(b.lastRefill).Seconds() > staleThreshold {
				delete(rl.limiters, key)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimitMiddleware returns an HTTP middleware that applies per-IP rate limiting
// for the given category. Returns 429 Too Many Requests when the limit is exceeded.
func RateLimitMiddleware(rl *RateLimiter, category string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.Allow(category, clientIP(r)) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
