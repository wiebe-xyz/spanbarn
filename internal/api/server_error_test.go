package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteServerErrorMapsCanceledTo499(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/attributes", nil)
	w := httptest.NewRecorder()
	writeServerError(w, r, "query failed", fmt.Errorf("list attributes: %w", context.Canceled))
	if w.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", w.Code, statusClientClosedRequest)
	}
}

func TestWriteServerErrorKeeps500ForRealFailures(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/attributes", nil)
	w := httptest.NewRecorder()
	writeServerError(w, r, "query failed", errors.New("disk on fire"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// A read that outran its budget is a 503 the web client retries: the cached
// reads keep scanning and the retry reads their result. The response must not
// be cached, or the retry would be served the 503 again.
func TestWriteServerErrorMapsTimeoutTo503(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cases := map[string]struct {
		r   *http.Request
		err error
	}{
		"deadline error": {httptest.NewRequest(http.MethodGet, "/api/v1/traces/groups", nil), fmt.Errorf("groups: %w", context.DeadlineExceeded)},
		// The driver can surface an interrupted scan as its own error; the
		// expired request context still marks it as a timeout.
		"expired request": {httptest.NewRequest(http.MethodGet, "/api/v1/traces/groups", nil).WithContext(expired), errors.New("interrupted (9)")},
	}
	for name, tc := range cases {
		w := httptest.NewRecorder()
		w.Header().Set("Cache-Control", "private, max-age=60")
		writeServerError(w, tc.r, "query failed", tc.err)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503", name, w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "2" {
			t.Errorf("%s: Retry-After = %q, want 2", name, got)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
		}
		if !strings.Contains(w.Body.String(), `"error":"query_timeout"`) {
			t.Errorf("%s: body = %s, want error query_timeout", name, w.Body.String())
		}
	}
}

func TestReadBudgetBoundsAPIReads(t *testing.T) {
	var hasDeadline bool
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
	})
	h := readBudgetMiddleware(5*time.Second, inner)
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v1/traces/groups", true},
		{http.MethodGet, "/api/v1/spans/live", false},
		{http.MethodGet, "/api/v1/export", false},
		{http.MethodPost, "/api/v1/boards", false},
		{http.MethodGet, "/assets/index.js", false},
	}
	for _, tc := range cases {
		hasDeadline = false
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
		if hasDeadline != tc.want {
			t.Errorf("%s %s: deadline = %v, want %v", tc.method, tc.path, hasDeadline, tc.want)
		}
	}
	hasDeadline = false
	readBudgetMiddleware(0, inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/traces/groups", nil))
	if hasDeadline {
		t.Error("a zero budget must add no deadline")
	}
}
