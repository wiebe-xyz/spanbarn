package rollup

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func testRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return repository.NewRepository(db.DB)
}

// counterRow is a cumulative counter sample for one instance series.
func counterRow(bucket time.Time, version string, last float64) repository.MetricRollup {
	return repository.MetricRollup{
		ProjectID: 1, Name: "reqs", Type: "sum", Unit: "1",
		Temporality:     "cumulative",
		AttrFingerprint: version,
		Attributes:      `{"service.name":"api","service.version":"` + version + `"}`,
		Bucket:          bucket.UTC(), Count: 1, Last: last,
	}
}

func newTestCompactor(t *testing.T, repo *repository.Repository, now time.Time) *Compactor {
	t.Helper()
	c := New(repo, Config{BucketsPerPass: 24}, slog.New(slog.DiscardHandler))
	c.now = func() time.Time { return now }
	return c
}

// TestCompactorBuildsHourlyTier walks the whole path: 5-minute rows in, one
// hourly row per reduced series out, watermark advanced, and the counter's
// running total continuing across hour boundaries.
func TestCompactorBuildsHourlyTier(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	// Two hours of one counter, reported by two builds of the same service.
	h1 := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	h2 := h1.Add(time.Hour)
	rows := []repository.MetricRollup{
		counterRow(h1, "v1", 10),
		counterRow(h1.Add(30*time.Minute), "v1", 40), // +30
		counterRow(h2, "v2", 5),
		counterRow(h2.Add(30*time.Minute), "v2", 25), // +20
	}
	if err := repo.UpsertMetricRollups(rows); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	c := newTestCompactor(t, repo, h2.Add(2*time.Hour))
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: StepHour,
		From: h1.Add(-time.Hour), To: h2.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("hourly buckets = %d, want 2", len(got))
	}
	// First hour: the opening sample has nothing before it, so only 40-10 counts.
	if got[0].Sum != 30 {
		t.Errorf("hour 1 increase = %v, want 30", got[0].Sum)
	}
	// Second hour is a different build, but the same reduced series: the running
	// total continues instead of dropping to that instance's own counter.
	if got[1].Sum != 20 {
		t.Errorf("hour 2 increase = %v, want 20", got[1].Sum)
	}
	if got[1].Last != got[0].Last+20 {
		t.Errorf("running total = %v, want %v (previous hour + increase)", got[1].Last, got[0].Last+20)
	}
	if got[0].AttrFingerprint != got[1].AttrFingerprint {
		t.Error("the two builds should reduce to one series once service.version is dropped")
	}

	through, ok, err := repo.MetricRollupWatermark(ctx, Step5m)
	if err != nil || !ok {
		t.Fatalf("watermark: ok=%v err=%v", ok, err)
	}
	if !through.After(h2) {
		t.Errorf("watermark = %s, want past the second hour", through)
	}
}

// TestCompactorLeavesOpenBucketAlone: a bucket still taking writes would be
// compacted twice and counted twice.
func TestCompactorLeavesOpenBucketAlone(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	h1 := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{counterRow(h1, "v1", 1)}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	// Now is inside the same hour.
	c := newTestCompactor(t, repo, h1.Add(30*time.Minute))
	if err := c.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: StepHour,
		From: h1.Add(-time.Hour), To: h1.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("compacted %d open buckets, want 0", len(got))
	}
}

// TestCompactorIsIdempotent: a repeated pass replaces its own output rather than
// adding to it, which is what makes an interrupted pass safe to redo.
func TestCompactorIsIdempotent(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()

	h1 := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{
		counterRow(h1, "v1", 10),
		counterRow(h1.Add(30*time.Minute), "v1", 40),
	}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	now := h1.Add(2 * time.Hour)
	if err := newTestCompactor(t, repo, now).RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	first, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: StepHour, From: h1, To: now,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	// A fresh compactor with no watermark redoes the same window.
	if err := repo.SetMetricRollupWatermark(ctx, Step5m, h1); err != nil {
		t.Fatalf("reset watermark: %v", err)
	}
	if err := newTestCompactor(t, repo, now).RunOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	second, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: StepHour, From: h1, To: now,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(second) != len(first) {
		t.Fatalf("rows after re-run = %d, want %d", len(second), len(first))
	}
	if second[0].Sum != first[0].Sum {
		t.Errorf("re-running doubled the bucket: %v then %v", first[0].Sum, second[0].Sum)
	}
}

// TestCompactorDisabledStopsImmediately: the kill switch has to stop compaction
// without stopping the writer, because the shape that makes compaction
// misbehave may only exist on one database. Run must return at once rather than
// sit on its ticker, so the goroutine is gone rather than merely idle.
func TestCompactorDisabledStopsImmediately(t *testing.T) {
	repo := testRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h1 := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{
		counterRow(h1, "v1", 10),
		counterRow(h1.Add(30*time.Minute), "v1", 40),
	}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	c := New(repo, Config{BucketsPerPass: 24, Disabled: true}, slog.New(slog.DiscardHandler))
	c.now = func() time.Time { return h1.Add(2 * time.Hour) }

	start := time.Now()
	c.Run(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Run took %s: it waited on its ticker instead of returning", elapsed)
	}

	got, err := repo.QueryCoarseRollups(ctx, repository.CoarseRollupFilter{
		ProjectID: 1, Name: "reqs", StepSeconds: StepHour, From: h1, To: h1.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("compacted %d buckets while disabled", len(got))
	}

	// The watermark must stay put, so retention keeps every tier intact.
	if _, ok, err := repo.MetricRollupWatermark(ctx, Step5m); err != nil {
		t.Fatalf("watermark: %v", err)
	} else if ok {
		t.Error("watermark advanced while compaction was disabled, which would let retention delete un-compacted data")
	}
}
