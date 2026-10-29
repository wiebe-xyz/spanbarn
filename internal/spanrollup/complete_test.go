package spanrollup

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// arrive stores one raw span ingested at at.
func arrive(t *testing.T, repo *repository.Repository, db *sql.DB, at time.Time) {
	t.Helper()
	id := fmt.Sprintf("s%d", at.UnixNano())
	if err := repo.InsertSpans([]repository.Span{{ProjectID: 1, TraceID: "t" + id, SpanID: id, Name: "GET /", Service: "api"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE spans SET ingested_at = ? WHERE span_id = ?`, at.UTC().String(), id); err != nil {
		t.Fatal(err)
	}
}

// rowsExcept stores one 5-minute row in each of the n buckets from h0, except
// the buckets in skip.
func rowsExcept(t *testing.T, repo *repository.Repository, h0 time.Time, n int, skip ...int) {
	t.Helper()
	var rows []repository.SpanRollup
	for i := range n {
		if contains(skip, i) {
			continue
		}
		rows = append(rows, fiveMinute(h0.Add(time.Duration(i)*5*time.Minute), "GET /", 100))
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func complete(t *testing.T, repo *repository.Repository, arrivals SpanArrivals, from, to, open time.Time) bool {
	t.Helper()
	ctx := context.Background()
	cov, err := repo.SpanRollupCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	segs, ok := Plan(from, to, 300, cov)
	if !ok {
		t.Fatalf("Plan does not cover %v..%v", from, to)
	}
	got, err := Complete(ctx, repo, arrivals, segs, open)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Complete finds a 5-minute bucket without rows in which spans arrived, which
// Plan alone takes for covered, and leaves the newest unflushed buckets alone.
func TestCompleteFindsAGapInsideTheTier(t *testing.T) {
	repo, db := testRepoDB(t)
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rowsExcept(t, repo, h0, 24, 13)             // 11:05 holds no rows
	arrive(t, repo, db, h0.Add(67*time.Minute)) // though a span arrived in it
	arrive(t, repo, db, h0.Add(122*time.Minute))
	open := h0.Add(3 * time.Hour)
	for _, tc := range []struct {
		name     string
		from, to time.Time
		open     time.Time
		want     bool
	}{
		{"the first hour", h0, h0.Add(time.Hour), open, true},
		{"across the gap", h0, h0.Add(2 * time.Hour), open, false},
		{"after the gap", h0.Add(70 * time.Minute), h0.Add(2 * time.Hour), open, true},
		{"buckets past the newest row, not flushed yet", h0.Add(70 * time.Minute), h0.Add(2*time.Hour + 10*time.Minute), h0.Add(2 * time.Hour), true},
		{"buckets past the newest row, flushed by now", h0.Add(70 * time.Minute), h0.Add(2*time.Hour + 10*time.Minute), open, false},
	} {
		if got := complete(t, repo, repo, tc.from, tc.to, tc.open); got != tc.want {
			t.Errorf("%s: complete = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A 5-minute bucket in which no span arrived at all, as while a deploy rolls
// the ingest pods, is quiet: the plan across it is complete. Without a way to
// ask about arrivals every empty bucket stays a gap.
func TestCompleteTakesAQuietBucketForCovered(t *testing.T) {
	repo, db := testRepoDB(t)
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rowsExcept(t, repo, h0, 24, 13, 20)
	arrive(t, repo, db, h0.Add(65*time.Minute-time.Second)) // the last second before the quiet bucket
	arrive(t, repo, db, h0.Add(70*time.Minute))             // the first second after it
	open := h0.Add(3 * time.Hour)
	if !complete(t, repo, repo, h0, h0.Add(2*time.Hour), open) {
		t.Error("plan across two quiet buckets is not complete")
	}
	if complete(t, repo, nil, h0, h0.Add(2*time.Hour), open) {
		t.Error("without arrivals, an empty bucket counts as covered")
	}
}

// More empty buckets than maxQuietBuckets in one segment read as rollups that
// were off, quiet or not.
func TestCompleteCapsQuietBuckets(t *testing.T) {
	repo := testRepo(t)
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	skip := make([]int, 0, maxQuietBuckets+1)
	for i := 1; i <= maxQuietBuckets+1; i++ {
		skip = append(skip, i)
	}
	rowsExcept(t, repo, h0, maxQuietBuckets+3, skip...)
	end := h0.Add(time.Duration(maxQuietBuckets+3) * 5 * time.Minute)
	if complete(t, repo, repo, h0, end, end) {
		t.Errorf("%d quiet buckets in one segment count as complete", maxQuietBuckets+1)
	}
}

// An hour with a 5-minute bucket that holds nothing while spans arrived in it
// is not compacted, so the gap stays visible in the hourly tier; the watermark
// still moves past it, and the repair pass leaves it out too.
func TestCompactorLeavesOutAnHourWithAGap(t *testing.T) {
	repo, db := testRepoDB(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rowsExcept(t, repo, h0, 24, 13)
	arrive(t, repo, db, h0.Add(67*time.Minute))
	now := h0.Add(2*time.Hour + 11*time.Minute)
	if err := newTestCompactor(repo, &now).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := window(t, repo, 3600, h0, h0.Add(time.Hour)); len(got) != 1 || got[0].Count != 12 {
		t.Errorf("first hour = %+v, want one row of 12 spans", got)
	}
	if got := window(t, repo, 3600, h0.Add(time.Hour), h0.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("hour with a gap = %+v, want no rows", got)
	}
	through, ok, err := repo.SpanRollupWatermark(ctx, 300)
	if err != nil || !ok || !through.Equal(h0.Add(2*time.Hour)) {
		t.Errorf("5m watermark = %v, %v, %v; want %v", through, ok, err, h0.Add(2*time.Hour))
	}
}

// An hour with a quiet 5-minute bucket is compacted from the buckets it has.
func TestCompactorBuildsAnHourWithAQuietBucket(t *testing.T) {
	repo := testRepo(t)
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rowsExcept(t, repo, h0, 24, 13)
	now := h0.Add(2*time.Hour + 11*time.Minute)
	if err := newTestCompactor(repo, &now).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := window(t, repo, 3600, h0.Add(time.Hour), h0.Add(2*time.Hour)); len(got) != 1 || got[0].Count != 11 {
		t.Errorf("hour with a quiet bucket = %+v, want one row of 11 spans", got)
	}
}

// Hours an older binary left out behind the watermark, because a quiet bucket
// read as a gap then, are built by the repair pass, and the day above them on
// the next pass.
func TestCompactorRepairsHoursLeftOutBehindTheWatermark(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	d0 := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	rowsExcept(t, repo, d0, 24*12, 13, 100)
	if err := repo.SetSpanRollupWatermark(ctx, 300, d0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	now := d0.Add(24*time.Hour + 11*time.Minute)
	c := newTestCompactor(repo, &now)
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	hours := window(t, repo, 3600, d0, d0.Add(24*time.Hour))
	if len(hours) != 24 || hours[0].Count != 12 || hours[1].Count != 11 || hours[8].Count != 11 {
		t.Fatalf("repaired hours = %d rows, want 24 with 11 spans at 01:00 and 08:00", len(hours))
	}
	if err := c.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	day := window(t, repo, 86400, d0, d0.Add(24*time.Hour))
	if len(day) != 1 || day[0].Count != 24*12-2 {
		t.Errorf("day = %+v, want one row of %d spans", day, 24*12-2)
	}
}
