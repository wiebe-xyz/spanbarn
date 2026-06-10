package aggregation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// mockAccWriter implements AggregateWriter and captures upserted aggregates.
type mockAccWriter struct {
	mu   sync.Mutex
	aggs []repository.Aggregate
}

func (m *mockAccWriter) UpsertAggregate(agg repository.Aggregate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aggs = append(m.aggs, agg)
	return nil
}

func (m *mockAccWriter) UpsertAggregates(aggs []repository.Aggregate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aggs = append(m.aggs, aggs...)
	return nil
}

func (m *mockAccWriter) get() []repository.Aggregate {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]repository.Aggregate, len(m.aggs))
	copy(cp, m.aggs)
	return cp
}

func TestAccumulatorAdd(t *testing.T) {
	w := &mockAccWriter{}
	acc := NewAccumulator(w, time.Minute, time.Hour, nil)

	now := time.Now().UTC()
	acc.Add(repository.Span{ProjectID: 1, Service: "svc", Name: "op", Kind: "server", StartTimeUs: now.UnixMicro(), DurationUs: 100})
	acc.Add(repository.Span{ProjectID: 1, Service: "svc", Name: "op", Kind: "server", StartTimeUs: now.UnixMicro(), DurationUs: 200, Status: "error"})

	acc.mu.Lock()
	if len(acc.slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(acc.slots))
	}
	for _, sl := range acc.slots {
		if sl.count != 2 {
			t.Errorf("count = %d, want 2", sl.count)
		}
		if sl.errorCount != 1 {
			t.Errorf("errorCount = %d, want 1", sl.errorCount)
		}
		if len(sl.durations) != 2 {
			t.Errorf("len(durations) = %d, want 2", len(sl.durations))
		}
	}
	acc.mu.Unlock()
}

func TestAccumulatorFlush(t *testing.T) {
	w := &mockAccWriter{}
	acc := NewAccumulator(w, time.Minute, time.Hour, nil)

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		acc.Add(repository.Span{
			ProjectID:   1,
			Service:     "svc",
			Name:        "op",
			Kind:        "server",
			StartTimeUs: now.UnixMicro(),
			DurationUs:  int64(100 * (i + 1)),
		})
	}

	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	aggs := w.get()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].Count != 5 {
		t.Errorf("Count = %d, want 5", aggs[0].Count)
	}
	if aggs[0].P50Us == 0 {
		t.Error("P50Us should be non-zero")
	}

	// Slots should be reset after flush.
	acc.mu.Lock()
	if len(acc.slots) != 0 {
		t.Errorf("slots not reset after flush, got %d", len(acc.slots))
	}
	acc.mu.Unlock()
}

func TestAccumulatorQueryRecent(t *testing.T) {
	w := &mockAccWriter{}
	acc := NewAccumulator(w, time.Minute, time.Hour, nil)

	now := time.Now().UTC()
	acc.Add(repository.Span{ProjectID: 1, Service: "alpha", Name: "get", Kind: "server", StartTimeUs: now.UnixMicro(), DurationUs: 50})
	acc.Add(repository.Span{ProjectID: 1, Service: "beta", Name: "post", Kind: "server", StartTimeUs: now.UnixMicro(), DurationUs: 100, Status: "error"})

	// No filter — returns all.
	all := acc.QueryRecent(repository.AggregateFilter{ProjectID: 1})
	if len(all) != 2 {
		t.Fatalf("QueryRecent (no filter) = %d rows, want 2", len(all))
	}

	// Service filter.
	only := acc.QueryRecent(repository.AggregateFilter{ProjectID: 1, Service: "alpha"})
	if len(only) != 1 || only[0].Service != "alpha" {
		t.Fatalf("QueryRecent (service=alpha) = %v, want 1 alpha row", only)
	}

	// ProjectID filter excludes all.
	none := acc.QueryRecent(repository.AggregateFilter{ProjectID: 99})
	if len(none) != 0 {
		t.Fatalf("QueryRecent (projectID=99) = %d rows, want 0", len(none))
	}
}

func TestAccumulatorAggregateSpansMatchesAggregator(t *testing.T) {
	w := &mockAccWriter{}
	acc := NewAccumulator(w, time.Minute, time.Hour, nil)
	agg := NewAggregator(w, time.Minute, nil)

	spans := []repository.Span{
		{ProjectID: 1, Service: "svc", Name: "op", Kind: "server", StartTimeUs: time.Now().UnixMicro(), DurationUs: 100},
		{ProjectID: 1, Service: "svc", Name: "op", Kind: "server", StartTimeUs: time.Now().UnixMicro(), DurationUs: 200, Status: "error"},
	}

	accAggs, err := acc.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("acc.AggregateSpans: %v", err)
	}
	aggAggs, err := agg.AggregateSpans(context.Background(), spans)
	if err != nil {
		t.Fatalf("agg.AggregateSpans: %v", err)
	}

	if len(accAggs) != len(aggAggs) {
		t.Fatalf("len(accAggs)=%d, len(aggAggs)=%d", len(accAggs), len(aggAggs))
	}
	if accAggs[0].Count != aggAggs[0].Count {
		t.Errorf("Count mismatch: acc=%d agg=%d", accAggs[0].Count, aggAggs[0].Count)
	}
	if accAggs[0].ErrorCount != aggAggs[0].ErrorCount {
		t.Errorf("ErrorCount mismatch: acc=%d agg=%d", accAggs[0].ErrorCount, aggAggs[0].ErrorCount)
	}
}
