package spanrollup

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

func testRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return repository.NewRepository(db.DB)
}

func newTestCompactor(repo Repository, now *time.Time) *Compactor {
	c := New(repo, Config{BucketsPerPass: 48}, slog.New(slog.DiscardHandler))
	c.now = func() time.Time { return *now }
	return c
}

// fiveMinute builds one 5-minute row of the given durations.
func fiveMinute(bucket time.Time, name string, durations ...int64) repository.SpanRollup {
	var h spanhist.Hist
	r := repository.SpanRollup{
		StepSeconds: 300, Bucket: bucket, ProjectID: 1, Service: "api", Name: name, Kind: "server",
		IsRoot: true, Status: "ok", MinUs: durations[0], MaxUs: durations[0],
	}
	for _, d := range durations {
		h.Observe(d)
		r.Count++
		r.WeightedCount += 4
		r.WeightedSpans++
		r.SumDurationUs += d
		r.MinUs, r.MaxUs = min(r.MinUs, d), max(r.MaxUs, d)
	}
	r.Hist = h.Encode()
	return r
}

func window(t *testing.T, repo *repository.Repository, step int64, from, to time.Time) []repository.SpanRollup {
	t.Helper()
	rows, err := repo.SpanRollupWindow(context.Background(), step, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Two hours of 5-minute rows compact into hourly rows equal to folding every
// duration of the hour at once, and the watermark moves past both hours.
func TestCompactorBuildsHourlyRowsEqualToDirectFold(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	var rows []repository.SpanRollup
	var all [2][]int64
	for i := range 24 {
		b := h0.Add(time.Duration(i) * 5 * time.Minute)
		d := []int64{int64(100 + i), int64(5000 * (i + 1))}
		rows = append(rows, fiveMinute(b, "GET /", d...))
		all[i/12] = append(all[i/12], d...)
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}

	now := h0.Add(2*time.Hour + 11*time.Minute)
	if err := newTestCompactor(repo, &now).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for hour := range 2 {
		start := h0.Add(time.Duration(hour) * time.Hour)
		got := window(t, repo, 3600, start, start.Add(time.Hour))
		if len(got) != 1 {
			t.Fatalf("hour %d: %d rows", hour, len(got))
		}
		want := fiveMinute(start, "GET /", all[hour]...)
		if got[0].Count != want.Count || got[0].SumDurationUs != want.SumDurationUs ||
			got[0].MinUs != want.MinUs || got[0].MaxUs != want.MaxUs ||
			got[0].WeightedCount != want.WeightedCount || string(got[0].Hist) != string(want.Hist) {
			t.Errorf("hour %d: got %+v, want %+v", hour, got[0], want)
		}
	}
	if wm, ok, _ := repo.SpanRollupWatermark(ctx, 300); !ok || !wm.Equal(h0.Add(2*time.Hour)) {
		t.Errorf("5m watermark = %v (set %v)", wm, ok)
	}
}

// An hour is compacted only once it ended Lag ago, and a day only once all of
// its hours are compacted.
func TestCompactorWaitsForLagAndLowerTier(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	var rows []repository.SpanRollup
	for i := range 24 {
		rows = append(rows, fiveMinute(day.Add(time.Duration(i)*time.Hour+55*time.Minute), "op", 10))
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}

	now := day.Add(24*time.Hour + 5*time.Minute) // the last hour ended 5m ago
	c := newTestCompactor(repo, &now)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := window(t, repo, 3600, day, day.Add(24*time.Hour)); len(got) != 23 {
		t.Errorf("hourly rows before the lag passed = %d, want 23", len(got))
	}
	if got := window(t, repo, 86400, day, day.Add(24*time.Hour)); len(got) != 0 {
		t.Errorf("daily row written before its last hour: %+v", got)
	}

	now = now.Add(10 * time.Minute)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	daily := window(t, repo, 86400, day, day.Add(24*time.Hour))
	if len(daily) != 1 || daily[0].Count != 24 {
		t.Fatalf("daily rows = %+v", daily)
	}
	if wm, ok, _ := repo.SpanRollupWatermark(ctx, 3600); !ok || !wm.Equal(day.Add(24*time.Hour)) {
		t.Errorf("hourly watermark = %v (set %v)", wm, ok)
	}
}

// Compacting the same bucket again leaves the same row: an interrupted pass
// that repeats a bucket does not count it twice.
func TestCompactBucketTwiceWritesTheSameRow(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	if err := repo.AddSpanRollups([]repository.SpanRollup{fiveMinute(h0, "op", 1, 2, 3)}); err != nil {
		t.Fatal(err)
	}
	now := h0.Add(3 * time.Hour)
	c := newTestCompactor(repo, &now)
	tier := Ladder()[0]
	for range 2 {
		if _, err := c.compactBucket(ctx, tier, h0, h0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if got := window(t, repo, 3600, h0, h0.Add(time.Hour)); len(got) != 1 || got[0].Count != 3 {
		t.Errorf("rows = %+v", got)
	}
}

func TestCompactorOnEmptyTableDoesNothing(t *testing.T) {
	repo := testRepo(t)
	now := time.Now()
	c := newTestCompactor(repo, &now)
	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.busy {
		t.Error("empty table reported backlog")
	}
}

func TestDisabledCompactorReturnsAtOnce(t *testing.T) {
	c := New(testRepo(t), Config{Disabled: true}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("disabled compactor kept running")
	}
}

func TestMergeSkipsAHistogramThatDoesNotDecode(t *testing.T) {
	b := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	good := fiveMinute(b, "op", 10, 20)
	bad := fiveMinute(b.Add(5*time.Minute), "op", 30)
	bad.Hist = []byte{7}
	other := fiveMinute(b, "other", 1)
	got := Merge([]repository.SpanRollup{good, bad, other}, 3600, b)
	if len(got) != 2 {
		t.Fatalf("rows = %+v", got)
	}
	h, err := spanhist.Decode(got[0].Hist)
	if err != nil || h.Count() != 2 || got[0].Count != 3 || got[0].MaxUs != 30 || got[0].StepSeconds != 3600 {
		t.Errorf("merged = %+v, hist count %d, err %v", got[0], h.Count(), err)
	}
}
