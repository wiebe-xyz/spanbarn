package repository

import (
	"testing"
	"time"
)

func TestUpsertAggregatesBatch(t *testing.T) {
	repo := setupTestDB(t)

	bucket := time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC)
	aggs := []Aggregate{
		{
			ProjectID: 1, Service: "api", Operation: "GET /a", Bucket: bucket,
			Count: 10, ErrorCount: 1, P50Us: 100, P95Us: 500, P99Us: 1000,
			MaxUs: 1200, SumDurationUs: 5000,
		},
		{
			ProjectID: 1, Service: "api", Operation: "GET /b", Bucket: bucket,
			Count: 5, P50Us: 200, P95Us: 800, P99Us: 1500,
			MaxUs: 1800, SumDurationUs: 2500,
		},
	}

	// Empty batch is a no-op.
	if err := repo.UpsertAggregates(nil); err != nil {
		t.Fatalf("UpsertAggregates(nil): %v", err)
	}

	if err := repo.UpsertAggregates(aggs); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}

	got, err := repo.QueryAggregates(AggregateFilter{
		ProjectID: 1,
		From:      bucket.Add(-time.Hour),
		To:        bucket.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 aggregates, got %d", len(got))
	}

	// Re-upsert the same operation: count should accumulate per ON CONFLICT clause.
	if err := repo.UpsertAggregates([]Aggregate{aggs[0]}); err != nil {
		t.Fatalf("UpsertAggregates re-upsert: %v", err)
	}
	got2, err := repo.QueryAggregates(AggregateFilter{
		ProjectID: 1,
		Service:   "api",
		Operation: "GET /a",
		From:      bucket.Add(-time.Hour),
		To:        bucket.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryAggregates filtered: %v", err)
	}
	if len(got2) != 1 || got2[0].Count != 20 {
		t.Fatalf("expected count to accumulate to 20, got %+v", got2)
	}
}
