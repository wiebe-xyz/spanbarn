package rollup

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// repoRow keeps the table-heavy tests readable.
type repoRow = repository.MetricRollup

var (
	hourTier  = Tier{Step: StepHour, Source: Step5m, Label: "hourly"}
	testStart = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
)

func src(name, fp, typ string, bucket time.Time, opts ...func(*rowBuilder)) repoRow {
	b := rowBuilder{row: repoRow{
		ProjectID:       7,
		Name:            name,
		Type:            typ,
		Unit:            "1",
		Temporality:     "cumulative",
		AttrFingerprint: fp,
		Attributes:      `{"service.name":"api","service.version":"` + fp + `"}`,
		Bucket:          bucket,
		Count:           1,
	}}
	for _, o := range opts {
		o(&b)
	}
	return b.row
}

type rowBuilder struct{ row repoRow }

func withLast(v float64) func(*rowBuilder) { return func(b *rowBuilder) { b.row.Last = v } }
func withSum(v float64) func(*rowBuilder)  { return func(b *rowBuilder) { b.row.Sum = v } }
func withDelta() func(*rowBuilder)         { return func(b *rowBuilder) { b.row.Temporality = "delta" } }
func withMinMax(lo, hi float64) func(*rowBuilder) {
	return func(b *rowBuilder) { b.row.Min, b.row.Max = lo, hi }
}
func withHist(sum float64, obs int64, counts ...float64) func(*rowBuilder) {
	return func(b *rowBuilder) {
		b.row.Sum, b.row.ObsCount = sum, obs
		e, _ := json.Marshal(histogram{Bounds: []float64{1, 5, 10}, Counts: counts})
		b.row.Extra = string(e)
	}
}

// TestMergeCounterAcrossDeploy is the case the increase-based merge exists for:
// two instance series where one ends mid-window and another starts. Adding their
// running totals would make the merged value fall, which rate derivation reads
// as a counter reset.
func TestMergeCounterAcrossDeploy(t *testing.T) {
	m5 := 5 * time.Minute
	rows := []repoRow{
		// old instance: 100 → 130 (increase 30), then gone
		src("reqs", "v1", "sum", testStart, withLast(100)),
		src("reqs", "v1", "sum", testStart.Add(m5), withLast(130)),
		// new instance: starts at 4, reaches 9 (increase 5 after its first sample)
		src("reqs", "v2", "sum", testStart.Add(2*m5), withLast(4)),
		src("reqs", "v2", "sum", testStart.Add(3*m5), withLast(9)),
	}
	carry := Carry{
		// what each series read just before the window
		SourceTail: map[string]repoRow{
			"reqs\x00v1": src("reqs", "v1", "sum", testStart.Add(-m5), withLast(90)),
		},
		TargetLast: map[string]float64{},
	}

	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), carry)
	if len(out) != 1 {
		t.Fatalf("service.version is dropped at the hourly tier, so both instances merge: got %d rows", len(out))
	}
	// v1: (100-90) + (130-100) = 40. v2: first sample contributes nothing, then 9-4 = 5.
	if got, want := out[0].Sum, 45.0; got != want {
		t.Errorf("increase = %v, want %v", got, want)
	}
	if got, want := out[0].Last, 45.0; got != want {
		t.Errorf("running total = %v, want %v (previous target bucket was empty)", got, want)
	}
}

// TestMergeCounterContinuesRunningTotal checks the synthesised cumulative value
// picks up from the previous target bucket, which is what keeps derive.Rate
// working unchanged on a coarse tier.
func TestMergeCounterContinuesRunningTotal(t *testing.T) {
	rows := []repoRow{
		src("reqs", "v1", "sum", testStart, withLast(10)),
		src("reqs", "v1", "sum", testStart.Add(5*time.Minute), withLast(25)),
	}
	_, fp := DefaultDropPolicy().Reduce(rows[0].Attributes, StepHour)
	carry := Carry{
		SourceTail: map[string]repoRow{"reqs\x00v1": src("reqs", "v1", "sum", testStart.Add(-5*time.Minute), withLast(10))},
		TargetLast: map[string]float64{fp: 1000},
	}

	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), carry)
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1", len(out))
	}
	if got, want := out[0].Sum, 15.0; got != want {
		t.Errorf("increase = %v, want %v", got, want)
	}
	if got, want := out[0].Last, 1015.0; got != want {
		t.Errorf("running total = %v, want %v", got, want)
	}
}

// TestMergeCounterReset: a value below its predecessor means the process
// restarted, so the new value is itself the increase since that restart.
func TestMergeCounterReset(t *testing.T) {
	rows := []repoRow{
		src("reqs", "v1", "sum", testStart, withLast(100)),
		src("reqs", "v1", "sum", testStart.Add(5*time.Minute), withLast(7)),
	}
	carry := Carry{
		SourceTail: map[string]repoRow{"reqs\x00v1": src("reqs", "v1", "sum", testStart.Add(-5*time.Minute), withLast(80))},
	}

	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), carry)
	if got, want := out[0].Sum, 27.0; got != want { // (100-80) + 7
		t.Errorf("increase across reset = %v, want %v", got, want)
	}
}

// TestMergeDeltaSumAdds: a delta series already holds per-bucket movement, so it
// is summed rather than differenced — and needs no carry row.
func TestMergeDeltaSumAdds(t *testing.T) {
	rows := []repoRow{
		src("reqs", "v1", "sum", testStart, withDelta(), withSum(3)),
		src("reqs", "v1", "sum", testStart.Add(5*time.Minute), withDelta(), withSum(4)),
	}
	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), Carry{})
	if got, want := out[0].Sum, 7.0; got != want {
		t.Errorf("delta sum = %v, want %v", got, want)
	}
}

// TestMergeGauge keeps count/sum for the average and extends min/max.
func TestMergeGauge(t *testing.T) {
	rows := []repoRow{
		src("mem", "v1", "gauge", testStart, withSum(10), withMinMax(2, 8)),
		src("mem", "v2", "gauge", testStart, withSum(20), withMinMax(1, 9)),
	}
	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), Carry{})
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1 merged series", len(out))
	}
	if out[0].Sum != 30 || out[0].Count != 2 {
		t.Errorf("sum/count = %v/%v, want 30/2", out[0].Sum, out[0].Count)
	}
	if out[0].Min != 1 || out[0].Max != 9 {
		t.Errorf("min/max = %v/%v, want 1/9", out[0].Min, out[0].Max)
	}
}

// TestMergeCumulativeHistogram differences the snapshots, so the coarse bucket
// holds the observations made during the window rather than a running total.
func TestMergeCumulativeHistogram(t *testing.T) {
	rows := []repoRow{
		src("dur", "v1", "histogram", testStart, withHist(150, 15, 5, 7, 3)),
		src("dur", "v1", "histogram", testStart.Add(5*time.Minute), withHist(260, 25, 9, 11, 5)),
	}
	carry := Carry{SourceTail: map[string]repoRow{
		"dur\x00v1": src("dur", "v1", "histogram", testStart.Add(-5*time.Minute), withHist(100, 10, 4, 4, 2)),
	}}

	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), carry)
	if len(out) != 1 {
		t.Fatalf("got %d rows, want 1", len(out))
	}
	if got, want := out[0].ObsCount, int64(15); got != want { // 25 - 10
		t.Errorf("obs_count = %d, want %d", got, want)
	}
	if got, want := out[0].Sum, 160.0; got != want { // 260 - 100
		t.Errorf("sum = %v, want %v", got, want)
	}
	var h histogram
	if err := json.Unmarshal([]byte(out[0].Extra), &h); err != nil {
		t.Fatalf("extra: %v", err)
	}
	// (5-4, 7-4, 3-2) + (9-5, 11-7, 5-3)
	for i, want := range []float64{5, 7, 3} {
		if h.Counts[i] != want {
			t.Errorf("bucket %d = %v, want %v", i, h.Counts[i], want)
		}
	}
}

// TestMergeWithoutCarryContributesNothing: with no earlier sample the movement
// inside the window is unknowable. Counting the running total in full would
// inflate every sparse series, and most series here report a few times a day.
func TestMergeWithoutCarryContributesNothing(t *testing.T) {
	rows := []repoRow{src("reqs", "v1", "sum", testStart, withLast(5000))}
	out := Merge(hourTier, testStart, 7, rows, DefaultDropPolicy(), Carry{})
	if got := out[0].Sum; got != 0 {
		t.Errorf("increase = %v, want 0 for a first-ever sample", got)
	}
}
