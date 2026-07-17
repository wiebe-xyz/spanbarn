package api

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// countingHandler counts WARN records emitted to it.
type countingHandler struct{ warns *int }

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.warns++
	}
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

func TestWarnOrphanedIngestThrottled(t *testing.T) {
	warns := 0
	logger := slog.New(countingHandler{warns: &warns})

	// Reset the process-wide throttle so this test is deterministic.
	lastOrphanWarnMinute.Store(0)

	// First call in this minute warns; subsequent calls in the same minute are
	// throttled.
	warnOrphanedIngest(logger, "svc-a")
	warnOrphanedIngest(logger, "svc-a")
	warnOrphanedIngest(logger, "svc-b")

	if warns != 1 {
		t.Fatalf("expected exactly 1 throttled warning, got %d", warns)
	}
}

func TestWarnOrphanedIngestNilLogger(t *testing.T) {
	// Must not panic with a nil logger.
	lastOrphanWarnMinute.Store(0)
	warnOrphanedIngest(nil, "svc")
}

func TestExtractRequestService(t *testing.T) {
	req := &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "checkout"}}},
			}}},
		},
	}
	if got := extractRequestService(req); got != "checkout" {
		t.Fatalf("want 'checkout', got %q", got)
	}

	// No service.name → empty string (not "unknown").
	empty := &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{Resource: &resourcepb.Resource{}}},
	}
	if got := extractRequestService(empty); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

// TestWarnOrphanedIngestCoversSelfExport pins that self-instrument spans landing
// in project 0 are reported. SPANBARN_SELF_API_KEY falls back to the global
// SPANBARN_API_KEY when unset, which authenticates as projectID 0 and dumps
// SpanBarn's own telemetry into the unattributed bucket — 911k spans in
// production, invisible because this warning used to skip selfExport.
func TestWarnOrphanedIngestCoversSelfExport(t *testing.T) {
	warns := 0
	logger := slog.New(countingHandler{warns: &warns})
	lastOrphanWarnMinute.Store(0)

	// The handler calls warnOrphanedIngest for ANY projectID 0 now, self or not.
	warnOrphanedIngest(logger, "spanbarn")

	if warns != 1 {
		t.Fatalf("expected the orphaned self-export to warn, got %d warnings", warns)
	}
}

// orphanCount reads the orphaned_ingest_total counter for one signal.
func orphanCount(t *testing.T, m *Metrics, signal string) float64 {
	t.Helper()
	var out dto.Metric
	c, err := m.OrphanedIngest.GetMetricWithLabelValues(signal)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	if err := c.(prometheus.Metric).Write(&out); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.GetCounter().GetValue()
}

// Project-0 ingest is accepted with a 200 and then hidden from every
// per-project view, so this counter is the only scrapeable evidence of the
// loss. The warn is only visible to someone reading SpanBarn's own logs.
func TestCountOrphanedIngest(t *testing.T) {
	s := &Server{metrics: NewMetrics()}

	s.countOrphanedIngest("traces", 3)
	s.countOrphanedIngest("traces", 2)
	s.countOrphanedIngest("metrics", 7)

	if got := orphanCount(t, s.metrics, "traces"); got != 5 {
		t.Errorf("traces = %v, want 5", got)
	}
	if got := orphanCount(t, s.metrics, "metrics"); got != 7 {
		t.Errorf("metrics = %v, want 7", got)
	}
	if got := orphanCount(t, s.metrics, "logs"); got != 0 {
		t.Errorf("logs = %v, want 0", got)
	}
}

// An empty batch is not evidence of orphaning; counting it would make
// rate(orphaned_ingest_total) fire on healthy no-op exports.
func TestCountOrphanedIngestIgnoresEmpty(t *testing.T) {
	s := &Server{metrics: NewMetrics()}
	s.countOrphanedIngest("traces", 0)
	if got := orphanCount(t, s.metrics, "traces"); got != 0 {
		t.Errorf("traces = %v, want 0", got)
	}
}

// Metrics are optional in several server modes; the guard must not panic.
func TestCountOrphanedIngestNilMetrics(t *testing.T) {
	s := &Server{}
	s.countOrphanedIngest("traces", 1)
}
