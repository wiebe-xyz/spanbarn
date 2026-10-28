package spanrollup

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Complete finds a 5-minute bucket without rows inside a plan, which Plan
// alone takes for covered, and leaves the newest unflushed buckets alone.
func TestCompleteFindsAGapInsideTheTier(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	var rows []repository.SpanRollup
	for i := range 24 {
		if i == 13 { // 11:05 holds nothing
			continue
		}
		rows = append(rows, fiveMinute(h0.Add(time.Duration(i)*5*time.Minute), "GET /", 100))
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}
	cov, err := repo.SpanRollupCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
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
		segs, ok := Plan(tc.from, tc.to, 300, cov)
		if !ok {
			t.Fatalf("%s: Plan does not cover it", tc.name)
		}
		got, err := Complete(ctx, repo, segs, tc.open)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("%s: complete = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An hour with a 5-minute bucket that holds nothing is not compacted, so the
// gap stays visible in the hourly tier; the watermark still moves past it.
func TestCompactorLeavesOutAnHourWithAGap(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	var rows []repository.SpanRollup
	for i := range 24 {
		if i == 13 {
			continue
		}
		rows = append(rows, fiveMinute(h0.Add(time.Duration(i)*5*time.Minute), "GET /", 100))
	}
	if err := repo.AddSpanRollups(rows); err != nil {
		t.Fatal(err)
	}
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
