package api

import (
	"context"
	"log/slog"
	"testing"

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
