package selfmetrics

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestReporterLogsRejectedExport guards against a regression where flush()
// closed the response body without checking its status code, so a failing
// self-metrics export (wrong API key, wrong project, server error) produced
// no log output at all — the exact failure mode that let a misconfigured
// SPANBARN_SELF_API_KEY (identical to the static admin key, routing every
// self-metric to project_id=0 instead of the "spanbarn" project) go
// unnoticed in production.
func TestReporterLogsRejectedExport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	rec := NewRecorder()
	rp := NewReporter(rec, srv.URL, "test-key", time.Second, nil, uint64(time.Now().UnixNano()), logger)
	rp.flush(context.Background())

	if !strings.Contains(logBuf.String(), "self-metrics export rejected") {
		t.Fatalf("expected a rejected-export log line, got: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "401") {
		t.Fatalf("expected the log line to include the rejected status code, got: %s", logBuf.String())
	}
}

// TestReporterSilentOnSuccess confirms a 2xx response produces no warning —
// the check added for the rejected-export case must not fire on the happy path.
func TestReporterSilentOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	rec := NewRecorder()
	rp := NewReporter(rec, srv.URL, "test-key", time.Second, nil, uint64(time.Now().UnixNano()), logger)
	rp.flush(context.Background())

	if strings.Contains(logBuf.String(), "rejected") || strings.Contains(logBuf.String(), "failed") {
		t.Fatalf("expected no warning on a successful export, got: %s", logBuf.String())
	}
}
