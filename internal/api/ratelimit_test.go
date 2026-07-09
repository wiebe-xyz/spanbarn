package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	newReq := func(remote, xff, xrip string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xrip != "" {
			r.Header.Set("X-Real-IP", xrip)
		}
		return r
	}

	t.Run("untrusted uses RemoteAddr and ignores headers", func(t *testing.T) {
		SetTrustProxy(false)
		got := clientIP(newReq("10.0.0.1:5555", "1.2.3.4", "9.9.9.9"))
		if got != "10.0.0.1" {
			t.Errorf("want 10.0.0.1 (peer, headers ignored), got %q", got)
		}
	})

	t.Run("trusted uses right-most XFF entry", func(t *testing.T) {
		SetTrustProxy(true)
		defer SetTrustProxy(false)
		// Left entry is client-forgeable; the right-most is proxy-appended.
		got := clientIP(newReq("10.0.0.1:5555", "6.6.6.6, 203.0.113.7", ""))
		if got != "203.0.113.7" {
			t.Errorf("want 203.0.113.7 (proxy-appended), got %q", got)
		}
	})

	t.Run("trusted falls back to X-Real-IP then peer", func(t *testing.T) {
		SetTrustProxy(true)
		defer SetTrustProxy(false)
		if got := clientIP(newReq("10.0.0.1:5555", "", "198.51.100.2")); got != "198.51.100.2" {
			t.Errorf("want X-Real-IP 198.51.100.2, got %q", got)
		}
		if got := clientIP(newReq("10.0.0.1:5555", "", "")); got != "10.0.0.1" {
			t.Errorf("want peer 10.0.0.1, got %q", got)
		}
	})
}

func newTestRateLimiter(loginRate, ingestRate, apiRate int) *RateLimiter {
	configs := map[string]float64{
		"login":  float64(loginRate) / 60.0,
		"ingest": float64(ingestRate) / 60.0,
		"api":    float64(apiRate) / 60.0,
	}
	return &RateLimiter{
		limiters: make(map[string]*bucket),
		configs:  configs,
		nowFunc:  time.Now,
	}
}

func TestRateLimiterAllow(t *testing.T) {
	rl := newTestRateLimiter(10, 600, 120)
	// Initial requests should succeed.
	for i := 0; i < 10; i++ {
		if !rl.Allow("login", "1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
}

func TestRateLimiterExhausted(t *testing.T) {
	rl := newTestRateLimiter(5, 600, 120)
	// Exhaust the login bucket (5 per minute).
	for i := 0; i < 5; i++ {
		if !rl.Allow("login", "1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	// Next request should be blocked.
	if rl.Allow("login", "1.2.3.4") {
		t.Fatal("request should be blocked after exhaustion")
	}
}

func TestRateLimiterRefill(t *testing.T) {
	now := time.Now()
	rl := newTestRateLimiter(60, 600, 120) // 60/min = 1/sec
	rl.nowFunc = func() time.Time { return now }

	// Exhaust 60 tokens.
	for i := 0; i < 60; i++ {
		if !rl.Allow("login", "1.2.3.4") {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if rl.Allow("login", "1.2.3.4") {
		t.Fatal("should be blocked")
	}

	// Advance time by 5 seconds => 5 tokens refilled.
	now = now.Add(5 * time.Second)
	rl.nowFunc = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if !rl.Allow("login", "1.2.3.4") {
			t.Fatalf("refill request %d should be allowed", i)
		}
	}
	if rl.Allow("login", "1.2.3.4") {
		t.Fatal("should be blocked after refill exhausted")
	}
}

func TestRateLimiterPerIP(t *testing.T) {
	rl := newTestRateLimiter(3, 600, 120)

	// Exhaust IP A.
	for i := 0; i < 3; i++ {
		rl.Allow("login", "10.0.0.1")
	}
	if rl.Allow("login", "10.0.0.1") {
		t.Fatal("IP A should be blocked")
	}

	// IP B should still be allowed.
	if !rl.Allow("login", "10.0.0.2") {
		t.Fatal("IP B should be allowed")
	}
}

func TestRateLimiterCategories(t *testing.T) {
	rl := newTestRateLimiter(3, 600, 120)

	// Exhaust login for this IP.
	for i := 0; i < 3; i++ {
		rl.Allow("login", "10.0.0.1")
	}
	if rl.Allow("login", "10.0.0.1") {
		t.Fatal("login should be blocked")
	}

	// Ingest for same IP should still work (different category, higher limit).
	if !rl.Allow("ingest", "10.0.0.1") {
		t.Fatal("ingest should be allowed for same IP")
	}
}

func TestRateLimitMiddleware429(t *testing.T) {
	rl := newTestRateLimiter(1, 600, 120)
	mw := RateLimitMiddleware(rl, "login")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request succeeds.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/login", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Second request should be rate limited.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "60" {
		t.Fatalf("expected Retry-After: 60, got %q", rr.Header().Get("Retry-After"))
	}
}
