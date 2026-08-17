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

// TestRepeatedExportFailuresLogOnce pins the fix for SPA-53, which accumulated
// 195 events for a condition nobody needs to act on: self-metrics are
// best-effort telemetry about ourselves, the ingest pod restarting is expected,
// and at a 30s interval every restart filed a fresh batch of BugBarn events.
//
// One line per outage, not one per interval. The first failure is still
// reported — silence would be its own bug.
func TestRepeatedExportFailuresLogOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	rp := NewReporter(NewRecorder(), srv.URL, "k", time.Second, nil, uint64(time.Now().UnixNano()), logger)

	for i := 0; i < 20; i++ {
		rp.flush(context.Background())
	}

	if got := strings.Count(logBuf.String(), "self-metrics export rejected"); got != 1 {
		t.Errorf("logged %d times for 20 consecutive failures, want 1", got)
	}
}

// TestExportRecoveryIsReported covers the other half: after a run of failures,
// success must say so and report how much was suppressed, or a silent recovery
// would leave the last visible line claiming the exporter is broken.
func TestExportRecoveryIsReported(t *testing.T) {
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	rp := NewReporter(NewRecorder(), srv.URL, "k", time.Second, nil, uint64(time.Now().UnixNano()), logger)

	for i := 0; i < 5; i++ {
		rp.flush(context.Background())
	}
	fail = false
	rp.flush(context.Background())

	out := logBuf.String()
	if !strings.Contains(out, "self-metrics export recovered") {
		t.Errorf("no recovery line after failures stopped: %s", out)
	}
	if !strings.Contains(out, "suppressed_failures=4") {
		t.Errorf("recovery line should report the 4 suppressed failures: %s", out)
	}

	// A later failure run must report again — suppression is per-run, not
	// permanent, or a second outage would be invisible.
	fail = true
	rp.flush(context.Background())
	if got := strings.Count(logBuf.String(), "self-metrics export rejected"); got != 2 {
		t.Errorf("second failure run logged %d times total, want 2", got)
	}
}

// TestSuccessfulExportIsQuiet guards the ordinary path: a healthy exporter
// should say nothing at all.
func TestSuccessfulExportIsQuiet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	rp := NewReporter(NewRecorder(), srv.URL, "k", time.Second, nil, uint64(time.Now().UnixNano()), logger)

	for i := 0; i < 3; i++ {
		rp.flush(context.Background())
	}
	if out := logBuf.String(); strings.TrimSpace(out) != "" {
		t.Errorf("a healthy exporter logged: %s", out)
	}
}
