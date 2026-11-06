package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

// TestSelfMetricsRecordRequestsUnderRoutePattern wires the recorder the way
// every mode does, after NewServer, and checks that a request reaches the
// exported latency histogram under its route pattern. The middleware chain used
// to capture the recorder while it was still nil, so no pod ever recorded a
// request and spanbarn.http.server.duration stayed empty in production.
func TestSelfMetricsRecordRequestsUnderRoutePattern(t *testing.T) {
	srv := api.NewServer(api.ServerConfig{APIKey: testAPIKey, Version: "test"}, nil, slog.Default())
	rec := selfmetrics.NewRecorder()
	srv.SetSelfMetricsRecorder(rec)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/api/v1/health", "/zz/0123456789abcdef"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	body := exportOnce(t, rec)
	if !strings.Contains(body, `"spanbarn.http.server.duration"`) {
		t.Fatalf("no request latency exported; payload: %.400s", body)
	}
	if !strings.Contains(body, `"http.route"`) || !strings.Contains(body, `"/api/v1/health"`) {
		t.Errorf("latency not labelled with the health route pattern; payload: %.600s", body)
	}
	if strings.Contains(body, "0123456789abcdef") {
		t.Error("a raw request path reached a metric label; only route patterns may")
	}
}

// exportOnce runs a reporter against a capture endpoint until its first export
// arrives and returns the exported JSON body.
func exportOnce(t *testing.T, rec *selfmetrics.Recorder) string {
	t.Helper()
	got := make(chan string, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case got <- string(b):
		default:
		}
	}))
	t.Cleanup(sink.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp := selfmetrics.NewReporter(rec, sink.URL, "k", 10*time.Millisecond, nil, 1, slog.Default())
	go rp.Run(ctx)
	select {
	case b := <-got:
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("reporter exported nothing")
		return ""
	}
}
