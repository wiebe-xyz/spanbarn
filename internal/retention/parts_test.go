package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// partsRepo records the span count of each delete and can stop the pass after a
// given delete.
type partsRepo struct {
	Repository
	parts  []int64
	stopAt int
	stop   context.CancelFunc
}

func (r *partsRepo) DeleteSpansByMaxIDRefreshing(ctx context.Context, maxID int64) (int64, error) {
	n, err := r.Repository.DeleteSpansByMaxIDRefreshing(ctx, maxID)
	r.parts = append(r.parts, n)
	if len(r.parts) == r.stopAt {
		r.stop()
	}
	return n, err
}

// insertPartSpans inserts count old spans of project 1.
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

// A batch is deleted in parts of retentionPart spans.
func TestRetentionBatchRunsInParts(t *testing.T) {
	worker, repo := setupTestWorker(t, partsConfig)
	rec := &partsRepo{Repository: worker.repo}
	worker.repo = rec
	insertPartSpans(t, repo, 2500)

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if fmt.Sprint(rec.parts) != "[1000 1000 500]" {
		t.Errorf("parts = %v, want [1000 1000 500]", rec.parts)
	}
	if got := spanCount(t, repo); got != 0 {
		t.Errorf("%d spans left, want 0", got)
	}
}

// A pass stopped between parts keeps the later parts' spans, and the next pass
// deletes them.
func TestRetentionStoppedBetweenPartsKeepsTheRest(t *testing.T) {
	worker, repo := setupTestWorker(t, partsConfig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &partsRepo{Repository: worker.repo, stopAt: 1, stop: cancel}
	worker.repo = rec
	insertPartSpans(t, repo, 2500)

	if err := worker.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce after a stop returned no error")
	}
	if left := spanCount(t, repo); left != 1500 {
		t.Fatalf("after the stop: %d spans left, want 1500", left)
	}

	rec.stopAt = 0
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if left := spanCount(t, repo); left != 0 {
		t.Errorf("after the rerun: %d spans left, want 0", left)
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
