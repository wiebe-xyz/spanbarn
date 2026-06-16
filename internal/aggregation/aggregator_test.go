package aggregation

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// mockWriter records calls to UpsertAggregate/UpsertAggregates.
type mockWriter struct {
	calls []repository.Aggregate
}

func (m *mockWriter) UpsertAggregate(agg repository.Aggregate) error {
	m.calls = append(m.calls, agg)
	return nil
}

func (m *mockWriter) UpsertAggregates(aggs []repository.Aggregate) error {
	m.calls = append(m.calls, aggs...)
	return nil
}

func newTestAggregator(w AggregateWriter) *Aggregator {
	return NewAggregator(w, time.Minute, slog.Default())
}

func baseSpan(service, name string, startUs, durationUs int64, status string) repository.Span {
	return repository.Span{
		ProjectID:   1,
		TraceID:     "trace-1",
		SpanID:      "span-1",
		Name:        name,
		Service:     service,
		Resource:    "/api",
		Kind:        "server",
		Status:      status,
		StartTimeUs: startUs,
		DurationUs:  durationUs,
	}
}

func TestAggregateSpans(t *testing.T) {
	agg := newTestAggregator(&mockWriter{})

	// 5 spans from service-a, 5 from service-b, all in the same minute bucket.
	bucket := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	startUs := bucket.UnixMicro()

	var spans []repository.Span
	for i := 0; i < 5; i++ {
		spans = append(spans, baseSpan("svc-a", "op1", startUs+int64(i), 100, "ok"))
	}
	for i := 0; i < 5; i++ {
		spans = append(spans, baseSpan("svc-b", "op2", startUs+int64(i), 200, "ok"))
	}

	result, err := agg.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("AggregateSpans: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(result))
	}

	for _, a := range result {
		if a.Count != 5 {
			t.Errorf("service %s: count = %d, want 5", a.Service, a.Count)
		}
	}
}

func TestAggregateSpansErrorCount(t *testing.T) {
	agg := newTestAggregator(&mockWriter{})

	bucket := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	startUs := bucket.UnixMicro()

	spans := []repository.Span{
		baseSpan("svc", "op", startUs, 100, "ok"),
		baseSpan("svc", "op", startUs+1, 100, "ok"),
		baseSpan("svc", "op", startUs+2, 100, "error"),
		baseSpan("svc", "op", startUs+3, 100, "error"),
		baseSpan("svc", "op", startUs+4, 100, "ok"),
	}

	result, err := agg.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("AggregateSpans: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 group, got %d", len(result))
	}
	if result[0].ErrorCount != 2 {
		t.Errorf("error_count = %d, want 2", result[0].ErrorCount)
	}
	if result[0].Count != 5 {
		t.Errorf("count = %d, want 5", result[0].Count)
	}
}

func TestAggregateSpansPercentiles(t *testing.T) {
	agg := newTestAggregator(&mockWriter{})

	bucket := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	startUs := bucket.UnixMicro()

	// 5 spans with known durations: 10, 20, 30, 40, 50
	spans := make([]repository.Span, 5)
	for i := range spans {
		spans[i] = baseSpan("svc", "op", startUs+int64(i), int64((i+1)*10), "ok")
	}

	result, err := agg.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("AggregateSpans: %v", err)
	}

	a := result[0]
	if a.P50Us != 30 {
		t.Errorf("p50 = %d, want 30", a.P50Us)
	}
	if a.MaxUs != 50 {
		t.Errorf("max = %d, want 50", a.MaxUs)
	}
	if a.SumDurationUs != 150 {
		t.Errorf("sum = %d, want 150", a.SumDurationUs)
	}
}

func TestAggregateSpansMultipleBuckets(t *testing.T) {
	agg := newTestAggregator(&mockWriter{})

	bucket1 := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	bucket2 := time.Date(2026, 5, 3, 10, 1, 0, 0, time.UTC)

	spans := []repository.Span{
		baseSpan("svc", "op", bucket1.UnixMicro(), 100, "ok"),
		baseSpan("svc", "op", bucket1.UnixMicro()+1, 200, "ok"),
		baseSpan("svc", "op", bucket2.UnixMicro(), 300, "ok"),
		baseSpan("svc", "op", bucket2.UnixMicro()+1, 400, "ok"),
	}

	result, err := agg.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("AggregateSpans: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(result))
	}

	// Each bucket has 2 spans.
	for _, a := range result {
		if a.Count != 2 {
			t.Errorf("bucket %v: count = %d, want 2", a.Bucket, a.Count)
		}
	}
}

func TestAggregateSpansEmptyInput(t *testing.T) {
	agg := newTestAggregator(&mockWriter{})

	result, err := agg.AggregateSpans(context.Background(), nil)
	if err != nil {
		t.Fatalf("AggregateSpans: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestPersist(t *testing.T) {
	mock := &mockWriter{}
	agg := newTestAggregator(mock)

	aggregates := []repository.Aggregate{
		{Service: "svc-a", Operation: "op1", Bucket: time.Now()},
		{Service: "svc-a", Operation: "op2", Bucket: time.Now()},
		{Service: "svc-b", Operation: "op3", Bucket: time.Now()},
	}

	if err := agg.Persist(context.Background(), aggregates); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	if len(mock.calls) != 3 {
		t.Errorf("UpsertAggregate called %d times, want 3", len(mock.calls))
	}
}
