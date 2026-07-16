package api_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// selfInstrumentUA must match observability.selfInstrumentUA — the User-Agent
// SpanBarn's own OTLP exporter sets, which TracingMiddleware turns into the
// context flag handleOTLP reads.
const selfInstrumentUA = "spanbarn-self-instrument"

// setupSampledOTLPServer builds a server wired the way the reader pod is: a
// TraceBuffer in front of the ingest queue. Returns the queue (what reached
// storage directly, bypassing sampling) and the buffer.
//
// The ingest handler flush loop is NOT started, so anything enqueued stays in
// the queue for inspection.
func setupSampledOTLPServer(t *testing.T, ratio int) (*httptest.Server, *ingest.Queue, *ingest.TraceBuffer) {
	t.Helper()
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := ingest.NewHandler(q, sp, 0, discard)
	tb := ingest.NewTraceBuffer(time.Hour, ingest.NewStaticRatioLookup(ratio), discard)

	srv := api.NewServerWithQuery(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, h, nil, nil, discard, api.WithTraceBuffer(tb))

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, q, tb
}

func postTraces(t *testing.T, ts *httptest.Server, status *tracepb.Status, userAgent string) {
	t.Helper()
	// buildOTLPRequest's trace ID starts 0x0102030405060708 = 72623859790382856,
	// which is not divisible by the 1000000 ratio used below — so it is dropped
	// unless something bypasses the sampler.
	otlpReq := buildOTLPRequest("spanbarn", "sqlite.insert", tracepb.Span_SPAN_KIND_CLIENT, status, 1700000000000000, 1700000001000000)
	body, err := proto.Marshal(otlpReq)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	if userAgent != "" {
		r.Header.Set("User-Agent", userAgent)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestSelfInstrumentSpansAreSampled is the regression test for the bug that let
// self-telemetry fill production's disk. Self-instrument spans used to bypass the
// TraceBuffer entirely, which made ingest.sample_ratio.project.N unenforceable for
// SpanBarn's own spans — by far the largest producer. They must go through the
// buffer like any other project.
func TestSelfInstrumentSpansAreSampled(t *testing.T) {
	ts, q, tb := setupSampledOTLPServer(t, 1000000)

	postTraces(t, ts, nil, selfInstrumentUA)

	if n := q.Len(); n != 0 {
		t.Fatalf("self-instrument spans bypassed the sampler: %d span(s) enqueued directly, want 0", n)
	}

	// They landed in the buffer; the ratio must then drop them.
	tb.Flush(time.Now().Add(2 * time.Hour))
	select {
	case spans := <-tb.Out:
		t.Fatalf("unsampled self trace was kept: %d span(s)", len(spans))
	case <-time.After(50 * time.Millisecond):
		// Correct: dropped by ratio.
	}
}

// TestSelfInstrumentErrorSpansAlwaysPass guards the original intent of the removed
// bypass: SpanBarn must never lose its own error traces, even at a sample ratio
// that drops everything else.
func TestSelfInstrumentErrorSpansAlwaysPass(t *testing.T) {
	ts, q, tb := setupSampledOTLPServer(t, 1000000)

	postTraces(t, ts, &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}, selfInstrumentUA)

	if n := q.Len(); n != 0 {
		t.Fatalf("expected the error trace to go through the buffer, got %d enqueued directly", n)
	}

	tb.Flush(time.Now().Add(2 * time.Hour))
	select {
	case spans := <-tb.Out:
		if len(spans) != 1 {
			t.Fatalf("want 1 self error span kept, got %d", len(spans))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("self-instrument error trace was dropped — errors must always pass")
	}
}

// TestNonSelfSpansStillSampled pins that ordinary tenant traffic is unchanged.
func TestNonSelfSpansStillSampled(t *testing.T) {
	ts, q, tb := setupSampledOTLPServer(t, 1000000)

	postTraces(t, ts, nil, "some-tenant-sdk/1.0")

	if n := q.Len(); n != 0 {
		t.Fatalf("tenant spans should go through the buffer, got %d enqueued directly", n)
	}
	tb.Flush(time.Now().Add(2 * time.Hour))
	select {
	case <-tb.Out:
		t.Fatal("unsampled tenant trace was kept")
	case <-time.After(50 * time.Millisecond):
	}
}

// warnSpy counts WARN records and keeps their messages.
type warnSpy struct {
	warns *int
	msgs  *[]string
}

func (h warnSpy) Enabled(context.Context, slog.Level) bool { return true }
func (h warnSpy) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.warns++
		*h.msgs = append(*h.msgs, r.Message)
	}
	return nil
}
func (h warnSpy) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h warnSpy) WithGroup(string) slog.Handler      { return h }

func newTestIngestHandler(t *testing.T) *ingest.Handler {
	t.Helper()
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	return ingest.NewHandler(ingest.NewQueue(16), sp, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestServerWarnsWhenIngestHasNoSampler is the runtime half of the guard (the
// compile-time half is TestEveryOTLPModeWiresTheSampler in cmd/spanbarn). A
// server that accepts spans with no TraceBuffer ingests everything unsampled and
// ignores every ingest.sample_ratio setting — silently, which is how
// runStandalone shipped for months.
func TestServerWarnsWhenIngestHasNoSampler(t *testing.T) {
	warns := 0
	var msgs []string
	logger := slog.New(warnSpy{warns: &warns, msgs: &msgs})

	api.NewServerWithQuery(api.ServerConfig{APIKey: testAPIKey, MaxBodyBytes: 1 << 20},
		newTestIngestHandler(t), nil, nil, logger)

	if warns == 0 {
		t.Fatal("a server accepting spans with no trace buffer did not warn — sampling is off and nothing says so")
	}
	joined := strings.Join(msgs, " ")
	if !strings.Contains(joined, "sampling is DISABLED") {
		t.Fatalf("warning does not say sampling is disabled: %v", msgs)
	}
}

// TestServerSilentWhenSamplerWired ensures the guard doesn't cry wolf on a
// correctly wired server.
func TestServerSilentWhenSamplerWired(t *testing.T) {
	warns := 0
	var msgs []string
	logger := slog.New(warnSpy{warns: &warns, msgs: &msgs})
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	tb := ingest.NewTraceBuffer(time.Hour, ingest.NewStaticRatioLookup(1), discard)
	api.NewServerWithQuery(api.ServerConfig{APIKey: testAPIKey, MaxBodyBytes: 1 << 20},
		newTestIngestHandler(t), nil, nil, logger, api.WithTraceBuffer(tb))

	for _, m := range msgs {
		if strings.Contains(m, "sampling is DISABLED") {
			t.Fatalf("correctly wired server warned about sampling: %v", msgs)
		}
	}
}
