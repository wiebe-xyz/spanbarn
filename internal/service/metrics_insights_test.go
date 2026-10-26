package service

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// insightsRepo records the scan Insights asks for and streams rows to it.
type insightsRepo struct {
	MetricsRepository
	through  time.Time
	hasMark  bool
	rows     []MetricRollup
	step     int64
	from, to time.Time
}

func (r *insightsRepo) MetricRollupWatermark(context.Context, int64) (time.Time, bool, error) {
	return r.through, r.hasMark, nil
}

func (r *insightsRepo) ScanProjectRollups(_ context.Context, _, step int64, from, to time.Time, fn func(MetricRollup) error) error {
	r.step, r.from, r.to = step, from, to
	for _, m := range r.rows {
		if err := fn(m); err != nil {
			return err
		}
	}
	return nil
}

// Windows under 12h read the 5-minute tier. Wider ones read the hourly tier
// up to the watermark, unless nothing in the window is compacted or the
// watermark lags into the recent part of the window.
func TestInsightsPicksTheTier(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	day := from.Add(24 * time.Hour)
	for _, tc := range []struct {
		name     string
		to       time.Time
		through  time.Time
		hasMark  bool
		wantStep int64
		wantTo   time.Time
	}{
		{"short window", from.Add(6 * time.Hour), day, true, 300, from.Add(6 * time.Hour)},
		{"wide window", day, day.Add(-90 * time.Minute), true, 3600, day.Add(-90*time.Minute - time.Second)},
		{"watermark past the end", day, day.Add(time.Hour), true, 3600, day},
		{"nothing compacted", day, time.Time{}, false, 300, day},
		{"watermark lags", day, day.Add(-10 * time.Hour), true, 300, day},
	} {
		repo := &insightsRepo{through: tc.through, hasMark: tc.hasMark}
		if _, err := NewMetricsService(repo).Insights(context.Background(), 1, from, tc.to); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if repo.step != tc.wantStep || !repo.from.Equal(from) || !repo.to.Equal(tc.wantTo) {
			t.Errorf("%s: scanned step %d %s..%s, want step %d to %s", tc.name, repo.step, repo.from, repo.to, tc.wantStep, tc.wantTo)
		}
	}
}

// Every series is judged, however many there are, and the largest changes
// come back first, capped at maxInsights.
func TestInsightsJudgesEverySeriesAndCaps(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var rows []MetricRollup
	for s := range maxInsights + 5 {
		for b := range 10 {
			v := 10.0
			if b >= 8 {
				v = 10 * float64(2+s) // a spike that grows with s
			}
			rows = append(rows, MetricRollup{Name: fmt.Sprintf("m%02d", s), Type: "gauge", AttrFingerprint: "fp", Attributes: "{}",
				Bucket: from.Add(time.Duration(b) * 30 * time.Minute), Count: 1, Sum: v, Last: v})
		}
	}
	got, err := NewMetricsService(&insightsRepo{rows: rows}).Insights(context.Background(), 1, from, from.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxInsights {
		t.Fatalf("%d insights, want %d", len(got), maxInsights)
	}
	if got[0].Metric != fmt.Sprintf("m%02d", maxInsights+4) || got[len(got)-1].Metric != "m05" {
		t.Errorf("order: first %s, last %s", got[0].Metric, got[len(got)-1].Metric)
	}
}
