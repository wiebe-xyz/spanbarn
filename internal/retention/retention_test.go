package retention

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// setupTestWorker creates an in-memory SQLite DB with migrations, repository,
// aggregator, and retention worker ready for testing.
func setupTestWorker(t *testing.T, cfg Config) (*RetentionWorker, *repository.Repository) {
	t.Helper()

	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	repo := repository.NewRepository(db.DB)
	logger := slog.Default()
	agg := aggregation.NewAggregator(repo, time.Minute, logger)
	worker := NewRetentionWorker(repo, agg, cfg, logger)

	return worker, repo
}

// insertTestSpans inserts spans into the repository with the given ingested_at timestamp.
// All spans belong to project 1.
func insertTestSpans(t *testing.T, repo *repository.Repository, count int, status string, durationUs int64, ingestedAt time.Time) {
	t.Helper()
	spans := make([]repository.Span, count)
	for i := 0; i < count; i++ {
		spans[i] = repository.Span{
			ProjectID:   1,
			TraceID:     fmt.Sprintf("trace-%s-%d", status, i),
			SpanID:      fmt.Sprintf("span-%s-%d", status, i),
			Name:        "test-op",
			Service:     "test-svc",
			Resource:    "/api/test",
			Kind:        "server",
			Status:      status,
			StartTimeUs: ingestedAt.UnixMicro(),
			DurationUs:  durationUs,
			Attributes:  "{}",
			Events:      "[]",
		}
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	// Update ingested_at to the desired time (InsertSpans uses DEFAULT CURRENT_TIMESTAMP).
	_, err := repo.DB().Exec(
		"UPDATE spans SET ingested_at = ? WHERE ingested_at > ?",
		ingestedAt, ingestedAt,
	)
	if err != nil {
		t.Fatalf("update ingested_at: %v", err)
	}
}

func TestRunOnceAggregatesOldSpans(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	// Create project.
	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert 10 old spans (2 hours ago).
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	insertTestSpans(t, repo, 10, "ok", 500, oldTime)

	// Run retention.
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Verify aggregates were created.
	aggs, err := repo.QueryAggregates(repository.AggregateFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	if len(aggs) == 0 {
		t.Fatal("expected aggregates to be created, got none")
	}

	// Verify old spans were deleted.
	spans, err := repo.QuerySpans(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("expected 0 spans after retention, got %d", len(spans))
	}
}

func TestRunOnceSamplesErrors(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	insertTestSpans(t, repo, 3, "ok", 500, oldTime)
	insertTestSpans(t, repo, 2, "error", 500, oldTime)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Verify error spans were sampled.
	samples, err := repo.QueryErrorSamples(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryErrorSamples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 error samples, got %d", len(samples))
	}
}

func TestRunOnceSamplesSlowSpans(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        500_000, // 500ms
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	// 3 normal spans (100ms) and 2 slow spans (1s).
	insertTestSpans(t, repo, 3, "ok", 100_000, oldTime)
	insertTestSpans(t, repo, 2, "ok", 1_000_000, oldTime)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	samples, err := repo.QueryErrorSamples(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryErrorSamples: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 slow span samples, got %d", len(samples))
	}
}

func TestRunOncePreservesRecentSpans(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert old spans (2h ago).
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	insertTestSpans(t, repo, 5, "ok", 500, oldTime)

	// Insert recent spans and explicitly set their ingested_at to now (UTC)
	// to avoid SQLite CURRENT_TIMESTAMP vs Go time.Now() timezone mismatches.
	recentSpans := []repository.Span{
		{ProjectID: 1, TraceID: "recent-1", SpanID: "r1", Name: "op", Service: "svc", Resource: "/", Kind: "server", Status: "ok", StartTimeUs: time.Now().UnixMicro(), DurationUs: 100, Attributes: "{}", Events: "[]"},
		{ProjectID: 1, TraceID: "recent-2", SpanID: "r2", Name: "op", Service: "svc", Resource: "/", Kind: "server", Status: "ok", StartTimeUs: time.Now().UnixMicro(), DurationUs: 200, Attributes: "{}", Events: "[]"},
	}
	if err := repo.InsertSpans(recentSpans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	// Explicitly set recent spans to a future-proof time well within retention.
	recentTime := time.Now().UTC()
	_, err := repo.DB().Exec(
		"UPDATE spans SET ingested_at = ? WHERE trace_id IN ('recent-1', 'recent-2')",
		recentTime,
	)
	if err != nil {
		t.Fatalf("update recent ingested_at: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// Recent spans should still be there.
	spans, err := repo.QuerySpans(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("expected 2 recent spans preserved, got %d", len(spans))
	}
}

func TestRunOnceDeletesOldErrorSamples(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert old error samples directly (31 days ago).
	oldSamples := []repository.Span{
		{ProjectID: 1, TraceID: "old-err-1", SpanID: "oe1", Name: "op", Service: "svc", Resource: "/", Kind: "server", Status: "error", StartTimeUs: 1000, DurationUs: 100, Attributes: "{}", Events: "[]"},
	}
	if err := repo.InsertErrorSamples(oldSamples); err != nil {
		t.Fatalf("InsertErrorSamples: %v", err)
	}

	// Backdate sampled_at.
	oldDate := time.Now().UTC().Add(-31 * 24 * time.Hour)
	_, err := repo.DB().Exec("UPDATE error_samples SET sampled_at = ?", oldDate)
	if err != nil {
		t.Fatalf("update sampled_at: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	samples, err := repo.QueryErrorSamples(repository.SpanFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryErrorSamples: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("expected 0 old error samples, got %d", len(samples))
	}
}

func TestRunOnceDeletesOldAggregates(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert an old aggregate (400 days ago).
	oldBucket := time.Now().UTC().Add(-400 * 24 * time.Hour)
	if err := repo.UpsertAggregate(repository.Aggregate{
		ProjectID:     1,
		Service:       "svc",
		Operation:     "op",
		Resource:      "/",
		Kind:          "server",
		Bucket:        oldBucket,
		Count:         100,
		ErrorCount:    5,
		P50Us:         1000,
		P95Us:         5000,
		P99Us:         9000,
		MaxUs:         10000,
		SumDurationUs: 200000,
	}); err != nil {
		t.Fatalf("UpsertAggregate: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	aggs, err := repo.QueryAggregates(repository.AggregateFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	if len(aggs) != 0 {
		t.Fatalf("expected 0 old aggregates, got %d", len(aggs))
	}
}

func TestRunOnceEmptyDatabase(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, _ := setupTestWorker(t, cfg)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce on empty DB should not error, got: %v", err)
	}
}

func TestRunOnceMultipleBatches(t *testing.T) {
	cfg := Config{
		FullRetentionHours:     1,
		ErrorRetentionDays:     30,
		AggregateRetentionDays: 365,
		SlowThresholdUS:        1_000_000,
	}
	worker, repo := setupTestWorker(t, cfg)

	if _, err := repo.CreateProject("test", "Test"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Insert more than defaultBatchSize (5000) spans.
	// Insert in chunks to avoid overly large transactions.
	oldTime := time.Now().UTC().Add(-2 * time.Hour)
	total := 5500
	chunkSize := 500
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		if end > total {
			end = total
		}
		count := end - i
		spans := make([]repository.Span, count)
		for j := 0; j < count; j++ {
			idx := i + j
			spans[j] = repository.Span{
				ProjectID:   1,
				TraceID:     fmt.Sprintf("trace-batch-%d", idx),
				SpanID:      fmt.Sprintf("span-batch-%d", idx),
				Name:        "batch-op",
				Service:     "batch-svc",
				Resource:    "/batch",
				Kind:        "server",
				Status:      "ok",
				StartTimeUs: oldTime.UnixMicro(),
				DurationUs:  100,
				Attributes:  "{}",
				Events:      "[]",
			}
		}
		if err := repo.InsertSpans(spans); err != nil {
			t.Fatalf("InsertSpans chunk %d: %v", i, err)
		}
	}

	// Backdate all ingested_at.
	_, err := repo.DB().Exec("UPDATE spans SET ingested_at = ?", oldTime)
	if err != nil {
		t.Fatalf("update ingested_at: %v", err)
	}

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// All spans should be deleted.
	spans, err := repo.QuerySpans(repository.SpanFilter{ProjectID: 1, Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("expected 0 spans after multi-batch retention, got %d", len(spans))
	}

	// Aggregates should exist.
	aggs, err := repo.QueryAggregates(repository.AggregateFilter{ProjectID: 1, Limit: 100})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	if len(aggs) == 0 {
		t.Fatal("expected aggregates after processing 5500 spans")
	}

	// Verify total count matches.
	var totalCount int64
	for _, a := range aggs {
		totalCount += a.Count
	}
	if totalCount != int64(total) {
		t.Fatalf("expected aggregate count %d, got %d", total, totalCount)
	}
}
