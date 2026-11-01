package rollup

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ladderStart and ladderEnd span the two months whose boundaries a week
// straddles (the weeks of 2026-11-30 and 2026-12-28) and end on a boundary a
// week does not straddle (Monday 2027-02-01).
var (
	ladderStart = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	ladderEnd   = time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
)

// ladderSamples is a cumulative series every five minutes across the ladder
// span. The rate moves with the hour so that a lower hour follows a higher one,
// which is what a second round of differencing reads as a restart.
func ladderSamples(typ string) []repository.MetricRollup {
	var rows []repository.MetricRollup
	var low, high float64
	for b := ladderStart; b.Before(ladderEnd); b = b.Add(5 * time.Minute) {
		low += float64(b.Hour()%3 + 1)
		high += float64(b.Day()%2 + 1)
		row := repository.MetricRollup{
			ProjectID: 1, Name: "ladder", Type: typ, Unit: "1",
			Temporality:     "cumulative",
			AttrFingerprint: "api",
			Attributes:      `{"service.name":"api"}`,
			Bucket:          b, Count: 1,
		}
		if typ == "histogram" {
			row.Sum = low*2 + high*7
			row.ObsCount = int64(low + high)
			extra, _ := json.Marshal(histogram{Bounds: []float64{5}, Counts: []float64{low, high}})
			row.Extra = string(extra)
		} else {
			row.Last = low + high
		}
		rows = append(rows, row)
	}
	return rows
}

// runLadder feeds the samples through the compactor a day at a time, so each
// tier is built when its buckets close, as it is on a live system.
func runLadder(t *testing.T, repo *repository.Repository, samples []repository.MetricRollup) {
	t.Helper()
	if err := repo.UpsertMetricRollups(samples); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
	c := New(repo, Config{BucketsPerPass: 1000}, slog.New(slog.DiscardHandler))
	for day := ladderStart; !day.After(ladderEnd.AddDate(0, 0, 1)); day = day.AddDate(0, 0, 1) {
		now := day.Add(time.Hour)
		c.now = func() time.Time { return now }
		if err := c.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce at %s: %v", now, err)
		}
	}
}

// tierTotals adds up what one tier stores for the ladder series.
type tierTotals struct {
	rows   int
	sum    float64
	obs    int64
	counts [2]float64
}

func ladderTier(t *testing.T, repo *repository.Repository, step int64) tierTotals {
	t.Helper()
	got, err := repo.QueryCoarseRollups(context.Background(), repository.CoarseRollupFilter{
		ProjectID: 1, Name: "ladder", StepSeconds: step,
		From: ladderStart.AddDate(0, -2, 0), To: ladderEnd.AddDate(0, 2, 0),
	})
	if err != nil {
		t.Fatalf("QueryCoarseRollups(%d): %v", step, err)
	}
	var out tierTotals
	for _, r := range got {
		out.rows++
		out.sum += r.Sum
		out.obs += r.ObsCount
		if h := histogramOf(r); h != nil {
			out.counts[0] += h.Counts[0]
			out.counts[1] += h.Counts[1]
		}
	}
	return out
}

// TestLadderKeepsHistogramObservations takes cumulative histogram samples up
// through hour, day, week and month. Every tier has to hold the observations of
// the rows beneath it, and the hour tier has to hold what the samples recorded.
func TestLadderKeepsHistogramObservations(t *testing.T) {
	repo := testRepo(t)
	samples := ladderSamples("histogram")
	runLadder(t, repo, samples)

	// The opening sample has nothing before it, so its running total stays out.
	first, last := samples[0], samples[len(samples)-1]
	want := tierTotals{
		sum: last.Sum - first.Sum,
		obs: last.ObsCount - first.ObsCount,
		counts: [2]float64{
			histogramOf(last).Counts[0] - histogramOf(first).Counts[0],
			histogramOf(last).Counts[1] - histogramOf(first).Counts[1],
		},
	}

	for _, tier := range Ladder() {
		got := ladderTier(t, repo, tier.Step)
		if got.rows == 0 {
			t.Errorf("%s tier: no rows", tier.Label)
			continue
		}
		if got.obs != want.obs {
			t.Errorf("%s tier obs_count = %d, want %d", tier.Label, got.obs, want.obs)
		}
		if got.sum != want.sum {
			t.Errorf("%s tier sum = %v, want %v", tier.Label, got.sum, want.sum)
		}
		if got.counts != want.counts {
			t.Errorf("%s tier buckets = %v, want %v", tier.Label, got.counts, want.counts)
		}
	}
}

// TestLadderCountsStraddlingWeeks runs a counter across two month boundaries that
// fall inside a week. The month tier has to add up to the week tier, so no
// straddling week drops out and none lands in two months.
func TestLadderCountsStraddlingWeeks(t *testing.T) {
	repo := testRepo(t)
	samples := ladderSamples("sum")
	runLadder(t, repo, samples)

	week, month := ladderTier(t, repo, StepWeek), ladderTier(t, repo, StepMonth)
	if week.rows != 10 {
		t.Errorf("weeks = %d, want 10", week.rows)
	}
	if month.rows != 3 {
		t.Errorf("months = %d, want 3", month.rows)
	}
	if month.sum != week.sum {
		t.Errorf("month tier sum = %v, week tier sum = %v", month.sum, week.sum)
	}
	if want := samples[len(samples)-1].Last - samples[0].Last; month.sum != want {
		t.Errorf("month tier sum = %v, want %v from the samples", month.sum, want)
	}
}

func TestSourceWindow(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	month := Tier{Step: StepMonth, Source: StepWeek}
	cases := []struct {
		name     string
		tier     Tier
		bucket   time.Time
		from, to time.Time
	}{
		{"month starting mid-week", month, day(2026, 11, 1), day(2026, 10, 26), day(2026, 11, 30)},
		{"month after a straddling week", month, day(2026, 12, 1), day(2026, 11, 30), day(2026, 12, 28)},
		{"month ending on a Sunday", month, day(2027, 1, 1), day(2026, 12, 28), day(2027, 2, 1)},
		{"month starting on a Monday", month, day(2027, 2, 1), day(2027, 2, 1), day(2027, 3, 1)},
		{"week", Tier{Step: StepWeek, Source: StepDay}, day(2026, 11, 30), day(2026, 11, 30), day(2026, 12, 7)},
		{"hour", hourTier, testStart, testStart, testStart.Add(time.Hour)},
	}
	for _, tc := range cases {
		from, to := sourceWindow(tc.tier, tc.bucket)
		if !from.Equal(tc.from) || !to.Equal(tc.to) {
			t.Errorf("%s: window = [%s, %s), want [%s, %s)", tc.name, from, to, tc.from, tc.to)
		}
	}
}
