package repository

import (
	"context"
	"testing"
	"time"
)

func coarseRow(step int64, bucket time.Time, sum float64) MetricRollup {
	return MetricRollup{
		ProjectID: 1, Name: "reqs", Type: "sum", Unit: "1", Temporality: "cumulative",
		AttrFingerprint: "fp", Attributes: `{"service.name":"api"}`,
		StepSeconds: step, Bucket: bucket.UTC(), Count: 1, Sum: sum, Last: sum,
	}
}

// TestUpsertCoarseRollupsReplaces: a compaction pass derives the same row from
// the same closed window every time, so re-running one must not double it.
// That is what makes an interrupted pass safe to redo.
func TestUpsertCoarseRollupsReplaces(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	bucket := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	for _, sum := range []float64{30, 30} {
		if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{coarseRow(3600, bucket, sum)}); err != nil {
			t.Fatalf("UpsertCoarseRollups: %v", err)
		}
	}

	got, err := repo.QueryCoarseRollups(ctx, CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: 3600,
		From: bucket.Add(-time.Hour), To: bucket.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	if got[0].Sum != 30 {
		t.Errorf("sum = %v, want 30 (replaced, not accumulated)", got[0].Sum)
	}
}

func TestQueryCoarseRollupsFiltersByTier(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	bucket := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{
		coarseRow(3600, bucket, 1),
		coarseRow(86400, bucket, 2),
	}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	got, err := repo.QueryCoarseRollups(ctx, CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: 86400,
		From: bucket.Add(-time.Hour), To: bucket.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 1 || got[0].Sum != 2 {
		t.Errorf("got %d rows (%+v), want only the daily one", len(got), got)
	}
}

// TestQueryCoarseRollupsKeepsNewestUnderLimit: a range wider than the limit must
// return its most recent buckets. Paging oldest-first meant a 30-day chart was
// filled entirely by the oldest rows and showed nothing current.
func TestQueryCoarseRollupsKeepsNewestUnderLimit(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	var rows []MetricRollup
	for i := 0; i < 5; i++ {
		rows = append(rows, coarseRow(3600, base.Add(time.Duration(i)*time.Hour), float64(i)))
	}
	if err := repo.UpsertCoarseRollups(ctx, rows); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	got, err := repo.QueryCoarseRollups(ctx, CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: 3600,
		From: base, To: base.Add(10 * time.Hour), Limit: 3,
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[0].Sum != 2 || got[2].Sum != 4 {
		t.Errorf("got buckets %v..%v, want the newest three in ascending order", got[0].Sum, got[2].Sum)
	}
}

func TestDeleteCoarseRollupsOlderThanTouchesOneTier(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-100 * 24 * time.Hour)

	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{
		coarseRow(3600, old, 1),
		coarseRow(86400, old, 2),
	}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	n, err := repo.DeleteCoarseRollupsOlderThan(ctx, 3600, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("DeleteCoarseRollupsOlderThan: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d rows, want 1", n)
	}

	var remaining int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM metric_rollups_coarse WHERE step_seconds = 86400`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("daily rows left = %d, want 1 — deleting one tier must not touch another", remaining)
	}
}

// TestMetricRollupWatermarkOnlyMovesForward: retention deletes up to this mark,
// so a pass that re-ran an earlier window must not hand back a licence to delete
// less than it already could.
func TestMetricRollupWatermarkOnlyMovesForward(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	later := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	if err := repo.SetMetricRollupWatermark(ctx, 300, later); err != nil {
		t.Fatalf("set watermark: %v", err)
	}
	if err := repo.SetMetricRollupWatermark(ctx, 300, later.Add(-2*time.Hour)); err != nil {
		t.Fatalf("set watermark backwards: %v", err)
	}

	got, ok, err := repo.MetricRollupWatermark(ctx, 300)
	if err != nil || !ok {
		t.Fatalf("watermark: ok=%v err=%v", ok, err)
	}
	if !got.Equal(later) {
		t.Errorf("watermark = %s, want it to stay at %s", got, later)
	}
}

func TestMetricRollupWatermarkAbsent(t *testing.T) {
	_, ok, err := setupTestDB(t).MetricRollupWatermark(context.Background(), 300)
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if ok {
		t.Error("a tier that was never compacted must report no watermark, so retention keeps everything")
	}
}

func TestOldestRollupBucketPerTier(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	if err := repo.UpsertMetricRollups([]MetricRollup{
		{ProjectID: 1, Name: "reqs", Type: "sum", AttrFingerprint: "fp", Attributes: "{}", Bucket: base.Add(2 * time.Hour)},
		{ProjectID: 1, Name: "reqs", Type: "sum", AttrFingerprint: "fp2", Attributes: "{}", Bucket: base},
	}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{coarseRow(3600, base.Add(24*time.Hour), 1)}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	got, ok, err := repo.OldestRollupBucket(ctx, Rollup5mStep)
	if err != nil || !ok {
		t.Fatalf("OldestRollupBucket(5m): ok=%v err=%v", ok, err)
	}
	if !got.Equal(base) {
		t.Errorf("oldest 5m bucket = %s, want %s", got, base)
	}

	got, ok, err = repo.OldestRollupBucket(ctx, 3600)
	if err != nil || !ok {
		t.Fatalf("OldestRollupBucket(hourly): ok=%v err=%v", ok, err)
	}
	if !got.Equal(base.Add(24 * time.Hour)) {
		t.Errorf("oldest hourly bucket = %s, want %s", got, base.Add(24*time.Hour))
	}
}

func TestOldestRollupBucketEmpty(t *testing.T) {
	_, ok, err := setupTestDB(t).OldestRollupBucket(context.Background(), Rollup5mStep)
	if err != nil {
		t.Fatalf("OldestRollupBucket: %v", err)
	}
	if ok {
		t.Error("an empty tier has no oldest bucket")
	}
}
