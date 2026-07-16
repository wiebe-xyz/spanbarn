package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recordSpans installs an in-memory tracer provider as the global one (which is
// what otelsql uses when no provider is passed) and returns a func listing the
// names of spans recorded so far.
func recordSpans(t *testing.T) func() []string {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	return func() []string {
		_ = tp.ForceFlush(context.Background())
		var names []string
		for _, s := range sr.Ended() {
			names = append(names, s.Name())
		}
		return names
	}
}

func sqliteSpanNames(names []string) []string {
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, "sqlite.") {
			out = append(out, n)
		}
	}
	return out
}

// TestInsertSpansEmitsNoSpans is the regression test for SpanBarn spamming
// itself: otelsql instruments every statement, so inserting a span used to emit
// a sqlite.insert span, which had to be inserted, emitting another. In
// production that recursion produced 876k sqlite.insert spans — 96% of
// SpanBarn's self-telemetry. Writing telemetry must not produce telemetry.
func TestInsertSpansEmitsNoSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	before := len(sqliteSpanNames(spanNames()))

	if err := repo.InsertSpansContext(context.Background(), []Span{{
		ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op",
		Service: "svc", Kind: "internal", Status: "ok",
		StartTimeUs: 1, DurationUs: 1, Attributes: "{}", Events: "[]",
	}}); err != nil {
		t.Fatalf("InsertSpansContext: %v", err)
	}

	got := sqliteSpanNames(spanNames())
	if len(got) != before {
		t.Fatalf("writing a span emitted %d sqlite span(s) %v — the feedback loop is back", len(got)-before, got[before:])
	}
}

// TestInsertLogsAndMetricsEmitNoSpans covers the other telemetry write paths on
// the same loop.
func TestInsertLogsAndMetricsEmitNoSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	before := len(sqliteSpanNames(spanNames()))

	if err := repo.UpsertAggregates([]Aggregate{{
		ProjectID: 1, Service: "svc", Operation: "op", Kind: "internal",
		Bucket: time.Now().UTC().Truncate(time.Minute), Count: 1,
	}}); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}

	got := sqliteSpanNames(spanNames())
	if len(got) != before {
		t.Fatalf("writing aggregates emitted %d sqlite span(s) %v", len(got)-before, got[before:])
	}
}

// TestReadsAreStillTraced pins that the suppression is scoped to the telemetry
// write paths — user-facing queries must stay instrumented, since a read cannot
// feed itself.
func TestReadsAreStillTraced(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)

	if _, err := repo.CreateProject("app", "App"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Limit: 10}); err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}

	if got := sqliteSpanNames(spanNames()); len(got) == 0 {
		t.Fatal("no sqlite spans recorded for reads — suppression is too broad, DB observability is lost")
	}
}
