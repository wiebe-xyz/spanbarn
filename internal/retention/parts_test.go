package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// partsAggregator records the span count of each AggregateSpans call and can
// stop the pass before a given Persist call.
type partsAggregator struct {
	Aggregator
	parts    []int
	persists int
	stopAt   int
	stop     context.CancelFunc
}

func (a *partsAggregator) AggregateSpans(ctx context.Context, spans []repository.Span) ([]repository.Aggregate, error) {
	a.parts = append(a.parts, len(spans))
	return a.Aggregator.AggregateSpans(ctx, spans)
}

func (a *partsAggregator) Persist(ctx context.Context, aggs []repository.Aggregate) error {
	a.persists++
	if a.persists == a.stopAt {
		a.stop()
		return ctx.Err()
	}
	return a.Aggregator.Persist(ctx, aggs)
}

// insertPartSpans inserts count old spans of project 1, each in its own
// aggregate key, so every span is one aggregate row.
func insertPartSpans(t *testing.T, repo *repository.Repository, count int) {
	t.Helper()
	old := time.Now().UTC().Add(-2 * time.Hour)
	spans := make([]repository.Span, count)
	for i := range spans {
		spans[i] = repository.Span{
			ProjectID: 1, TraceID: fmt.Sprintf("part-trace-%d", i), SpanID: fmt.Sprintf("part-span-%d", i),
			Name: "part-op", Service: "part-svc", Resource: fmt.Sprintf("/part/%d", i), Kind: "server",
			Status: "ok", StartTimeUs: old.UnixMicro(), DurationUs: 100, Attributes: "{}", Events: "[]",
		}
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	if _, err := repo.DB().Exec("UPDATE spans SET ingested_at = ?", old); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func aggregatedCount(t *testing.T, repo *repository.Repository) int64 {
	t.Helper()
	var n int64
	if err := repo.DB().QueryRow("SELECT COALESCE(SUM(count), 0) FROM aggregates").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func spanCount(t *testing.T, repo *repository.Repository) int64 {
	t.Helper()
	var n int64
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM spans").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var partsConfig = Config{
	InterestingRetentionHours: 1,
	ErrorRetentionDays:        30,
	AggregateRetentionDays:    365,
	SlowThresholdUS:           1_000_000,
}

// A batch is aggregated and deleted in parts of retentionPart spans.
func TestRetentionBatchRunsInParts(t *testing.T) {
	worker, repo := setupTestWorker(t, partsConfig)
	rec := &partsAggregator{Aggregator: worker.aggregator}
	worker.aggregator = rec
	insertPartSpans(t, repo, 2500)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if fmt.Sprint(rec.parts) != "[1000 1000 500]" {
		t.Errorf("parts = %v, want [1000 1000 500]", rec.parts)
	}
	if got := aggregatedCount(t, repo); got != 2500 {
		t.Errorf("aggregated %d spans, want 2500", got)
	}
	if got := spanCount(t, repo); got != 0 {
		t.Errorf("%d spans left, want 0", got)
	}
}

// A pass stopped before the second part's aggregates are written keeps the
// unaggregated spans, and the next pass counts every span exactly once.
func TestRetentionStoppedBetweenPartsCountsOnce(t *testing.T) {
	worker, repo := setupTestWorker(t, partsConfig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &partsAggregator{Aggregator: worker.aggregator, stopAt: 2, stop: cancel}
	worker.aggregator = rec
	insertPartSpans(t, repo, 2500)

	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce after a stop returned no error")
	}
	if got, left := aggregatedCount(t, repo), spanCount(t, repo); got != 1000 || left != 1500 {
		t.Fatalf("after the stop: aggregated %d, %d spans left; want 1000 and 1500", got, left)
	}

	rec.stopAt = 0
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if got, left := aggregatedCount(t, repo), spanCount(t, repo); got != 2500 || left != 0 {
		t.Errorf("after the rerun: aggregated %d, %d spans left; want 2500 and 0", got, left)
	}
}

func TestSpanPartsSortsByID(t *testing.T) {
	batch := []repository.Span{{ID: 5}, {ID: 1}, {ID: 4}, {ID: 2}, {ID: 3}}
	parts := spanParts(batch, 2)
	got := ""
	for _, p := range parts {
		got += "["
		for _, s := range p {
			got += fmt.Sprint(s.ID)
		}
		got += "]"
	}
	if got != "[12][34][5]" {
		t.Errorf("parts = %s, want [12][34][5]", got)
	}
	if batch[0].ID != 5 {
		t.Error("spanParts reordered the caller's batch")
	}
}
