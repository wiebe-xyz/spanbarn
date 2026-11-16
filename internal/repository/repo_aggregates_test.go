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

func TestAggregatesKeepEnvironmentsApart(t *testing.T) {
	repo := setupTestDB(t)

	bucket := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	row := func(env string, count int64) Aggregate {
		return Aggregate{
			ProjectID: 1, Environment: env, Service: "api", Operation: "GET /a", Kind: "server",
			Bucket: bucket, Count: count, SumDurationUs: count * 100,
		}
	}
	if err := repo.UpsertAggregates([]Aggregate{row("production", 10), row("staging", 3), row("", 4)}); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}
	// The same bucket again adds into the same environment's row only.
	if err := repo.UpsertAggregates([]Aggregate{row("staging", 2)}); err != nil {
		t.Fatalf("UpsertAggregates again: %v", err)
	}

	read := func(env string) map[string]int64 {
		t.Helper()
		got, err := repo.QueryAggregates(AggregateFilter{ProjectID: 1, Environment: env, From: bucket.Add(-time.Hour), To: bucket.Add(time.Hour)})
		if err != nil {
			t.Fatalf("QueryAggregates(%q): %v", env, err)
		}
		out := map[string]int64{}
		for _, a := range got {
			out[a.Environment] += a.Count
		}
		return out
	}

	if got := read(""); got["production"] != 10 || got["staging"] != 5 || got[""] != 4 || len(got) != 3 {
		t.Fatalf("unscoped read = %v, want production 10, staging 5, unset 4", got)
	}
	if got := read("all"); len(got) != 3 {
		t.Fatalf("all read = %v, want every environment", got)
	}
	if got := read("staging"); len(got) != 1 || got["staging"] != 5 {
		t.Fatalf("staging read = %v, want staging 5 only", got)
	}
	if got := read("production"); len(got) != 2 || got["production"] != 10 || got[""] != 4 {
		t.Fatalf("production read = %v, want production 10 plus the unset rows", got)
	}
}
