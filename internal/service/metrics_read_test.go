package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// readRepo serves raw rows and per-step rollups, and records the rollup reads.
type readRepo struct {
	MetricsRepository
	raw     []MetricRow
	fine    []MetricRollup
	coarse  map[int64][]MetricRollup
	through time.Time
	hasMark bool
	err     error

	fineReads   []MetricRollupFilter
	coarseReads []CoarseRollupFilter
}

func (r *readRepo) QueryMetricSeries(context.Context, MetricFilter) ([]MetricRow, error) {
	return r.raw, r.err
}

func (r *readRepo) QueryMetricRollups(_ context.Context, f MetricRollupFilter) ([]MetricRollup, error) {
	r.fineReads = append(r.fineReads, f)
	return r.fine, r.err
}

func (r *readRepo) QueryCoarseRollups(_ context.Context, f CoarseRollupFilter) ([]MetricRollup, error) {
	r.coarseReads = append(r.coarseReads, f)
	return r.coarse[f.StepSeconds], r.err
}

func (r *readRepo) MetricRollupWatermark(context.Context, int64) (time.Time, bool, error) {
	return r.through, r.hasMark, r.err
}

var readBase = time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

func gaugeRollup(at time.Time, v float64) MetricRollup {
	return MetricRollup{Name: "g", Type: "gauge", Unit: "1", Attributes: `{"svc":"a"}`, Bucket: at, Count: 1, Sum: v, Last: v}
}

// TestRollupTierStepsPicksResolutionForRange: each range reads the finest tier
// that still gives a readable chart, with coarser tiers behind it as fallbacks.
// The fallbacks matter because the fine tiers are kept for days while the coarse
// ones go back years, so an old range has no fine buckets left to read.
func TestRollupTierStepsPicksResolutionForRange(t *testing.T) {
	tests := []struct {
		name     string
		width    time.Duration
		wantHead int64
		wantLen  int
	}{
		{"just past the raw threshold", 7 * time.Hour, tier.Step5m, 3},
		{"two days", 48 * time.Hour, tier.Step5m, 3},
		{"a week", 7 * 24 * time.Hour, tier.StepHour, 3},
		{"thirty days", 30 * 24 * time.Hour, tier.StepHour, 3},
		{"three months", 90 * 24 * time.Hour, tier.StepDay, 3},
		{"a year", 365 * 24 * time.Hour, tier.StepDay, 3},
		{"three years", 3 * 365 * 24 * time.Hour, tier.StepWeek, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupTierSteps(tc.width)
			if len(got) != tc.wantLen {
				t.Fatalf("steps = %v, want %d entries", got, tc.wantLen)
			}
			if got[0] != tc.wantHead {
				t.Errorf("first choice = %d, want %d", got[0], tc.wantHead)
			}
			for i := 1; i < len(got); i++ {
				if got[i] <= got[i-1] {
					t.Errorf("fallbacks must get coarser, got %v", got)
				}
			}
		})
	}
}

// A filtered read wider than a day, and a totals read from a day on, skip the
// 5-minute tier.
func TestTierStepsForReadSkipsTheFineTierWhenWide(t *testing.T) {
	filter := map[string]string{"svc": "a"}
	for name, tc := range map[string]struct {
		width  time.Duration
		labels map[string]string
		totals bool
		want   int64
	}{
		"filtered, a day":      {FilteredFineTierWidth, filter, false, tier.Step5m},
		"filtered, two days":   {48 * time.Hour, filter, false, tier.StepHour},
		"unfiltered, two days": {48 * time.Hour, nil, false, tier.Step5m},
		"totals, a day":        {FilteredFineTierWidth, nil, true, tier.StepHour},
		"totals, under a day":  {12 * time.Hour, nil, true, tier.Step5m},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tierStepsForRead(tc.width, tc.labels, tc.totals)[0]; got != tc.want {
				t.Errorf("first step = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReadInputRawPointsFirstOnANarrowRange(t *testing.T) {
	repo := &readRepo{raw: []MetricRow{
		{Name: "g", Type: "gauge", Unit: "ms", TimeUnixNano: 10, Value: 3, Attributes: `{"svc":"a"}`},
		{Name: "g", Type: "sum", Unit: "ms", TimeUnixNano: 40, Value: 5, StartTimeUnixNano: 20, Extra: `{"temporality":"delta"}`},
	}}
	got, err := NewMetricsService(repo).ReadInput(context.Background(), MetricInputRequest{Name: "g", From: readBase, To: readBase.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != 0 || got.Type != "gauge" || got.Unit != "ms" || len(got.Points) != 2 {
		t.Fatalf("got step %d type %q unit %q with %d points, want raw gauge in ms with 2", got.Step, got.Type, got.Unit, len(got.Points))
	}
	if got.Points[0].Attributes["svc"] != "a" {
		t.Errorf("attributes = %v, want svc=a", got.Points[0].Attributes)
	}
	if d := got.Points[1]; !d.Delta || d.Extra != nil || d.IntervalNano != 20 {
		t.Errorf("delta point = %+v, want delta with interval 20 and no extra", d)
	}
	if len(repo.fineReads)+len(repo.coarseReads) != 0 {
		t.Error("a narrow range with raw points read rollups")
	}
}

// A narrow range whose raw points expired reads the rollups, and a metric that
// reported nothing anywhere reads empty.
func TestReadInputNarrowRangeFallsBackToRollups(t *testing.T) {
	repo := &readRepo{fine: []MetricRollup{gaugeRollup(readBase, 4)}}
	svc := NewMetricsService(repo)
	req := MetricInputRequest{Name: "g", From: readBase.Add(-time.Hour), To: readBase.Add(time.Hour)}
	got, err := svc.ReadInput(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != tier.Step5m || len(got.Points) != 1 || got.Points[0].Value != 4 || got.Type != "gauge" {
		t.Errorf("got %+v, want one 5-minute gauge point of 4", got)
	}

	empty, err := NewMetricsService(&readRepo{}).ReadInput(context.Background(), req)
	if err != nil || empty.Step != 0 || len(empty.Points) != 0 {
		t.Errorf("empty read = %+v, %v, want nothing", empty, err)
	}
}

// A read that finds nothing at the first tier tries the next, and a delta bucket
// takes its interval from the row's own width or the tier's.
func TestReadInputWalksTheLadderAndSetsDeltaIntervals(t *testing.T) {
	delta := MetricRollup{Name: "c", Type: "sum", Temporality: "delta", Bucket: readBase, Sum: 7, Count: 1}
	repo := &readRepo{coarse: map[int64][]MetricRollup{tier.StepHour: {delta}}}
	got, err := NewMetricsService(repo).ReadInput(context.Background(), MetricInputRequest{
		Name: "c", From: readBase.Add(-10 * time.Hour), To: readBase, Totals: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != tier.StepHour || len(got.Points) != 1 {
		t.Fatalf("got step %d with %d points, want the hour tier with 1", got.Step, len(got.Points))
	}
	if p := got.Points[0]; !p.Delta || p.Value != 7 || p.IntervalNano != int64(time.Hour) {
		t.Errorf("point = %+v, want delta 7 over an hour", p)
	}
	if len(repo.fineReads) != 0 {
		t.Error("a totals read from a day on read the 5-minute tier first")
	}
}

// The hour tier read takes the buckets the compactor has not reached from the
// 5-minute tier, from the watermark on, so no bucket is read from both (#497).
func TestReadInputStitchesTheFineTailOntoTheHourTier(t *testing.T) {
	through := readBase
	repo := &readRepo{
		fine:    []MetricRollup{gaugeRollup(through, 2), gaugeRollup(through.Add(5*time.Minute), 2)},
		coarse:  map[int64][]MetricRollup{tier.StepHour: {gaugeRollup(through.Add(-time.Hour), 1)}},
		through: through, hasMark: true,
	}
	got, err := NewMetricsService(repo).ReadInput(context.Background(), MetricInputRequest{
		Name: "g", From: through.Add(-47 * time.Hour), To: through.Add(time.Hour), Labels: map[string]string{"svc": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Step != tier.StepHour || len(got.Points) != 3 {
		t.Fatalf("got step %d with %d points, want the hour tier with 3", got.Step, len(got.Points))
	}
	if !repo.fineReads[0].From.Equal(through) {
		t.Errorf("tail read from %s, want the watermark %s", repo.fineReads[0].From, through)
	}
	if want := through.Add(-time.Hour); !repo.coarseReads[0].To.Equal(want) {
		t.Errorf("hour read to %s, want %s", repo.coarseReads[0].To, want)
	}
	if got.Points[0].T != through.Add(-time.Hour).UnixNano() || got.Points[2].T != through.Add(5*time.Minute).UnixNano() {
		t.Errorf("points not oldest first: %v", got.Points)
	}
}

// With a limit, a tail that fills it is the whole answer, and one that does not
// leaves the hour tier the rest.
func TestHourTierWithTailAppliesTheLimit(t *testing.T) {
	through := readBase
	f := MetricRollupFilter{Name: "g", From: through.Add(-47 * time.Hour), To: through.Add(time.Hour), Limit: 2}
	repo := &readRepo{
		fine:    []MetricRollup{gaugeRollup(through, 2), gaugeRollup(through.Add(5*time.Minute), 2)},
		coarse:  map[int64][]MetricRollup{tier.StepHour: {gaugeRollup(through.Add(-time.Hour), 1)}},
		through: through, hasMark: true,
	}
	svc := NewMetricsService(repo)
	rows, err := svc.hourTierWithTail(context.Background(), f, f.From)
	if err != nil || len(rows) != 2 || len(repo.coarseReads) != 0 {
		t.Fatalf("full tail: %d rows, %d hour reads, %v; want 2 rows and no hour read", len(rows), len(repo.coarseReads), err)
	}

	f.Limit = 3
	if _, err = svc.hourTierWithTail(context.Background(), f, f.From); err != nil {
		t.Fatal(err)
	}
	if got := repo.coarseReads[0].Limit; got != 1 {
		t.Errorf("hour read limit = %d, want the 1 the tail left", got)
	}

	// A range that ends before the tail has room for an hour bucket reads only the tail.
	short := MetricRollupFilter{Name: "g", From: through, To: through.Add(10 * time.Minute)}
	repo.coarseReads = nil
	rows, err = svc.hourTierWithTail(context.Background(), short, short.From)
	if err != nil || len(rows) != 2 || len(repo.coarseReads) != 0 {
		t.Errorf("short range: %d rows, %d hour reads, %v; want the tail only", len(rows), len(repo.coarseReads), err)
	}
}

func TestReadInputReturnsRepositoryErrors(t *testing.T) {
	boom := errors.New("boom")
	svc := NewMetricsService(&readRepo{err: boom})
	for name, req := range map[string]MetricInputRequest{
		"raw":    {Name: "g", From: readBase, To: readBase.Add(time.Hour)},
		"wide":   {Name: "g", From: readBase.Add(-10 * time.Hour), To: readBase},
		"filter": {Name: "g", From: readBase.Add(-47 * time.Hour), To: readBase, Labels: map[string]string{"a": "b"}},
	} {
		if _, err := svc.ReadInput(context.Background(), req); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want the repository error", name, err)
		}
	}
}

// The tail never reaches back further than FineTailWidth, never starts before
// the watermark, and starts no earlier than the bucket holding from.
func TestTailStartIsBounded(t *testing.T) {
	to := readBase
	for name, tc := range map[string]struct {
		through time.Time
		ok      bool
		from    time.Time
		want    time.Time
	}{
		"no watermark":          {time.Time{}, false, to.Add(-48 * time.Hour), to.Add(-FineTailWidth)},
		"watermark inside":      {to.Add(-time.Hour), true, to.Add(-48 * time.Hour), to.Add(-time.Hour)},
		"watermark far behind":  {to.Add(-30 * time.Hour), true, to.Add(-48 * time.Hour), to.Add(-FineTailWidth)},
		"from after the cutoff": {to.Add(-time.Hour), true, to.Add(-30 * time.Minute), to.Add(-30 * time.Minute)},
	} {
		if got := tailStart(to, tc.through, tc.ok, tc.from); !got.Equal(tc.want) {
			t.Errorf("%s: tail starts at %s, want %s", name, got, tc.want)
		}
	}
	odd := time.Date(2026, 6, 16, 12, 20, 0, 0, time.UTC)
	if got, want := tailStart(odd, time.Time{}, false, odd.Add(-48*time.Hour)), time.Date(2026, 6, 16, 7, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("unaligned end: tail starts at %s, want %s", got, want)
	}
}
