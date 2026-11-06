package retention

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestRunOnceTracesPhasesWithCounts pins that a retention cycle's trace holds
// the cycle and its phase spans with db.calls counts, and no span per
// statement. One prod cycle on 2026-10-29 stored 2996 spans, nearly all of
// them sqlite.* children.
func TestRunOnceTracesPhasesWithCounts(t *testing.T) {
	ended := recordSpans(t)

	w, repo := setupTestWorker(t, Config{InterestingRetentionHours: 1})
	insertTestSpans(t, repo, 20, "ok", 1000, time.Now().Add(-2*time.Hour))
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range ended() {
		if strings.HasPrefix(s.Name(), "retention.") {
			byName[s.Name()] = s
		}
	}
	cycle, ok := byName["retention.cycle"]
	if !ok {
		t.Fatal("no retention.cycle span")
	}
	var inCycle []string
	for _, s := range ended() {
		if s.SpanContext().TraceID() == cycle.SpanContext().TraceID() && strings.HasPrefix(s.Name(), "sqlite.") {
			inCycle = append(inCycle, s.Name())
		}
	}
	if len(inCycle) > 0 {
		t.Fatalf("cycle trace holds %d sqlite span(s): %v", len(inCycle), inCycle)
	}

	var total int64
	for _, name := range []string{"retention.drain_spans", "retention.purge_span_derived", "retention.purge_housekeeping"} {
		s, ok := byName[name]
		if !ok {
			t.Fatalf("no %s span", name)
		}
		if s.Parent().SpanID() != cycle.SpanContext().SpanID() {
			t.Errorf("%s is not a child of retention.cycle", name)
		}
		for _, kv := range s.Attributes() {
			if kv.Key == "db.calls" {
				total += kv.Value.AsInt64()
			}
		}
	}
	if total == 0 {
		t.Fatal("phase spans carry no db.calls: the cycle's statements were neither traced nor counted")
	}
}

var (
	recorderOnce sync.Once
	recorder     *tracetest.SpanRecorder
)

// recordSpans returns the spans that end from this call on. The global
// tracer binds its delegate on the first SetTracerProvider only, so one
// recorder serves every test in the package. No test here runs in parallel.
func recordSpans(t *testing.T) func() []sdktrace.ReadOnlySpan {
	t.Helper()
	recorderOnce.Do(func() {
		recorder = tracetest.NewSpanRecorder()
		otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	})
	start := len(recorder.Ended())
	return func() []sdktrace.ReadOnlySpan { return recorder.Ended()[start:] }
}

func hasCancelled(s sdktrace.ReadOnlySpan) bool {
	for _, kv := range s.Attributes() {
		if kv.Key == "cancelled" && kv.Value.AsBool() {
			return true
		}
	}
	return false
}

// A pod shut down in the middle of a retention cycle finishes the batch in
// flight, starts no further phase and records the cycle as cancelled with no
// error span. Two rollouts on 2026-11-05 each left an error span: the drain
// ended its last batch while the yield waited on the cancelled context, and the
// next phase then failed at once with "context canceled".
func TestShutdownMidCycleIsNoError(t *testing.T) {
	ended := recordSpans(t)
	w, repo := setupTestWorker(t, Config{InterestingRetentionHours: 1})
	// A yield this long only ends early on the cancel, so the test hangs if
	// the drain ignores it.
	w.cfg.BatchYield = time.Hour
	insertTestSpans(t, repo, 20, "ok", 1000, time.Now().Add(-2*time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.afterBatch = cancel // SIGTERM lands right after the first batch

	if err := w.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce = %v, want context.Canceled", err)
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range ended() {
		if !strings.HasPrefix(s.Name(), "retention.") {
			continue
		}
		byName[s.Name()] = s
		if s.Status().Code == codes.Error {
			t.Errorf("%s has error status %q after a shutdown", s.Name(), s.Status().Description)
		}
	}
	cycle, ok := byName["retention.cycle"]
	if !ok {
		t.Fatal("no retention.cycle span")
	}
	if !hasCancelled(cycle) {
		t.Error("retention.cycle does not carry cancelled=true")
	}
	if _, ok := byName["retention.drain_spans"]; !ok {
		t.Error("no retention.drain_spans span for the batch that ran")
	}
	for _, name := range []string{"retention.purge_span_derived", "retention.purge_housekeeping"} {
		if _, ok := byName[name]; ok {
			t.Errorf("%s started after the cancel", name)
		}
	}
	left, err := repo.CountSpansOlderThan(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSpansOlderThan: %v", err)
	}
	if left != 0 {
		t.Errorf("%d old spans left: the batch in flight did not finish", left)
	}
}

// Only a cancel escapes the error status. A deadline and any other failure
// stay error spans, so a real retention failure still reaches barn-watch.
func TestPhaseOutcomeStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		want      codes.Code
		cancelled bool
	}{
		{"ok", nil, codes.Unset, false},
		{"cancelled", fmt.Errorf("purge logs: %w", context.Canceled), codes.Unset, true},
		{"deadline", fmt.Errorf("purge logs: %w", context.DeadlineExceeded), codes.Error, false},
		{"failed", errors.New("disk I/O error"), codes.Error, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ended := recordSpans(t)
			got := runPhase(context.Background(), "retention.test_phase", func(context.Context) error { return tc.err })
			if !errors.Is(got, tc.err) {
				t.Fatalf("runPhase returned %v, want %v", got, tc.err)
			}
			spans := ended()
			if len(spans) != 1 {
				t.Fatalf("%d spans ended, want 1", len(spans))
			}
			if c := spans[0].Status().Code; c != tc.want {
				t.Errorf("status %v, want %v", c, tc.want)
			}
			if got := hasCancelled(spans[0]); got != tc.cancelled {
				t.Errorf("cancelled = %v, want %v", got, tc.cancelled)
			}
		})
	}
}
