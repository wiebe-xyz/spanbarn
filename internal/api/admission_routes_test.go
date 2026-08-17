package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

// setupAdmissionServer builds a real server whose admission controller reports
// the given volume-used fraction.
func setupAdmissionServer(t *testing.T, used float64) *httptest.Server {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	h := ingest.NewHandler(q, sp, 0, slog.Default())
	h.Start(t.Context())
	t.Cleanup(func() { h.Stop() })

	adm := api.NewAdmission(func(context.Context) (float64, bool) { return used, true }, 0.95)
	srv := api.NewServerWithQuery(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, h, nil, nil, slog.Default(), api.WithAdmission(adm))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postSpans(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spans", strings.NewReader(validSpanJSON()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post spans: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func getHealth(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestAdmissionShedsTelemetryButKeepsControlPlane is the regression test for
// the August 2026 outage. When the volume filled, SQLITE_FULL took down *every*
// write at once — including the web_sessions insert behind OIDC login — so the
// operator lost the dashboard at exactly the moment they needed it, and the
// database could not be recovered in place.
//
// The fix is asymmetric on purpose: telemetry is refused, the control plane is
// not. Dropping spans is cheap and recoverable; losing login is neither.
func TestAdmissionShedsTelemetryButKeepsControlPlane(t *testing.T) {
	t.Run("healthy volume accepts telemetry", func(t *testing.T) {
		ts := setupAdmissionServer(t, 0.20)
		if got := postSpans(t, ts); got != http.StatusAccepted {
			t.Errorf("POST /api/v1/spans = %d, want 202", got)
		}
		if got := getHealth(t, ts); got != http.StatusOK {
			t.Errorf("GET /api/v1/health = %d, want 200", got)
		}
	})

	t.Run("nearly-full volume refuses telemetry", func(t *testing.T) {
		ts := setupAdmissionServer(t, 0.99)
		if got := postSpans(t, ts); got != http.StatusServiceUnavailable {
			t.Errorf("POST /api/v1/spans = %d, want 503 under disk pressure", got)
		}
	})

	// Capacity is internal state. The gate sits behind auth so an anonymous
	// caller cannot probe how full our disk is.
	t.Run("unauthenticated caller gets 401, not 503", func(t *testing.T) {
		ts := setupAdmissionServer(t, 0.99)
		resp, err := ts.Client().Post(ts.URL+"/api/v1/spans", "application/json",
			strings.NewReader(validSpanJSON()))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated POST = %d, want 401 — admission must not "+
				"leak capacity state to anonymous callers", resp.StatusCode)
		}
	})

	t.Run("nearly-full volume still serves the control plane", func(t *testing.T) {
		ts := setupAdmissionServer(t, 0.99)
		// The whole point: the routes an operator needs must not be gated.
		if got := getHealth(t, ts); got != http.StatusOK {
			t.Errorf("GET /api/v1/health = %d, want 200 — the control plane must "+
				"survive the condition that makes ingest unsafe", got)
		}
	})
}
