package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/session"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

var rangeTestBase = time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

func seedRangeRollups(t *testing.T, repo *repository.Repository, in5m, inHour bool) {
	t.Helper()
	row := func(step int64) repository.MetricRollup {
		return repository.MetricRollup{
			ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "a", Attributes: `{"svc":"a"}`,
			Bucket: rangeTestBase, StepSeconds: step, Count: 1, Sum: 1, Last: 1,
		}
	}
	if in5m {
		if err := repo.UpsertMetricRollups([]repository.MetricRollup{row(tier.Step5m)}); err != nil {
			t.Fatalf("UpsertMetricRollups: %v", err)
		}
	}
	if inHour {
		if err := repo.UpsertCoarseRollups(context.Background(), []repository.MetricRollup{row(tier.StepHour)}); err != nil {
			t.Fatalf("UpsertCoarseRollups: %v", err)
		}
	}
}

func readRange(t *testing.T, srv *Server, sm *session.Service, from, to time.Time, labels map[string]string) metricSeriesResponse {
	t.Helper()
	q := url.Values{"project_id": {"1"}, "name": {"g"}, "from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
	for k, v := range labels {
		q.Set("label["+k+"]", v)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/series?"+q.Encode(), nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp metricSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// A narrow range whose raw points have expired returned no series with status
// 200, while a wider range over the same hours returned rollups.
func TestNarrowRangePastRawRetentionReadsRollups(t *testing.T) {
	for name, tc := range map[string]struct {
		in5m, inHour bool
		from, to     time.Time
		wantStep     int64
	}{
		"5m tier still holds it": {true, true, rangeTestBase.Add(-time.Hour), rangeTestBase.Add(time.Hour), tier.Step5m},
		"only the hour tier":     {false, true, rangeTestBase.Add(-time.Hour), rangeTestBase.Add(time.Hour), tier.StepHour},
		// The range starts after the hour bucket does and ends before the next one.
		"narrower than a bucket": {false, true, rangeTestBase.Add(10 * time.Minute), rangeTestBase.Add(50 * time.Minute), tier.StepHour},
	} {
		t.Run(name, func(t *testing.T) {
			srv, sm, repo := setupMetricsQueryServer(t)
			seedRangeRollups(t, repo, tc.in5m, tc.inHour)
			resp := readRange(t, srv, sm, tc.from, tc.to, nil)
			if len(resp.Series) != 1 {
				t.Fatalf("%d series, want the rollup series", len(resp.Series))
			}
			if resp.Step != tc.wantStep {
				t.Errorf("step_seconds = %d, want %d", resp.Step, tc.wantStep)
			}
		})
	}
}

// Raw points stay the first choice on a narrow range, and a metric that reported
// nothing still reads empty.
func TestNarrowRangePrefersRawPoints(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC()
	seedRangeRollups(t, repo, true, true)
	insertTestMetrics(t, repo, []model.MetricRecord{{
		ProjectID: 1, Name: "g", Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Value: 1,
		Attributes: json.RawMessage(`{"svc":"a"}`),
	}})
	resp := readRange(t, srv, sm, now.Add(-time.Hour), now.Add(time.Hour), nil)
	if len(resp.Series) != 1 || resp.Step != 0 {
		t.Errorf("%d series at step %d, want the raw series at step 0", len(resp.Series), resp.Step)
	}

	resp = readRange(t, srv, sm, rangeTestBase.Add(-48*time.Hour), rangeTestBase.Add(-47*time.Hour), nil)
	if len(resp.Series) != 0 || resp.Step != 0 {
		t.Errorf("%d series at step %d on a range with no data, want none", len(resp.Series), resp.Step)
	}
}

// A selective label filter over about two days scanned every 5-minute row in the
// range and timed out, so a filtered read that wide starts at the hour tier.
func TestFilteredReadTierFollowsTheRangeWidth(t *testing.T) {
	filter := map[string]string{"svc": "a"}
	for name, tc := range map[string]struct {
		width    time.Duration
		labels   map[string]string
		wantStep int64
	}{
		"filtered, a day":      {service.FilteredFineTierWidth, filter, tier.Step5m},
		"filtered, two days":   {48 * time.Hour, filter, tier.StepHour},
		"unfiltered, two days": {48 * time.Hour, nil, tier.Step5m},
	} {
		t.Run(name, func(t *testing.T) {
			srv, sm, repo := setupMetricsQueryServer(t)
			seedRangeRollups(t, repo, true, true)
			from := rangeTestBase.Add(-tc.width / 2)
			resp := readRange(t, srv, sm, from, from.Add(tc.width), tc.labels)
			if len(resp.Series) != 1 {
				t.Fatalf("%d series, want 1", len(resp.Series))
			}
			if resp.Step != tc.wantStep {
				t.Errorf("step_seconds = %d, want %d", resp.Step, tc.wantStep)
			}
		})
	}
}

// seedTailRollups stores one gauge bucket of value v per entry of at, in the
// 5-minute tier or the hour tier.
func seedTailRollups(t *testing.T, repo *repository.Repository, step int64, v float64, at ...time.Time) {
	t.Helper()
	rows := make([]repository.MetricRollup, 0, len(at))
	for _, b := range at {
		rows = append(rows, repository.MetricRollup{
			ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "a", Attributes: `{"svc":"a"}`,
			Bucket: b, StepSeconds: step, Count: 1, Sum: v, Last: v,
		})
	}
	var err error
	if step == tier.Step5m {
		err = repo.UpsertMetricRollups(rows)
	} else {
		err = repo.UpsertCoarseRollups(context.Background(), rows)
	}
	if err != nil {
		t.Fatalf("seed step %d: %v", step, err)
	}
}

// pointTimes lists the bucket times of a response's only series.
func pointTimes(t *testing.T, resp metricSeriesResponse) []time.Time {
	t.Helper()
	if len(resp.Series) != 1 {
		t.Fatalf("%d series, want 1", len(resp.Series))
	}
	var out []time.Time
	for _, p := range resp.Series[0].Points {
		out = append(out, time.Unix(0, p.T).UTC())
	}
	return out
}

// A filtered read wider than a day starts at the hour tier, and the hours the
// compactor has not reached yet were missing from it (#497). The read now takes
// those from the 5-minute tier, from the hour tier's watermark on, so no bucket
// is read twice.
func TestWideFilteredReadKeepsTheNewestHour(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	through := rangeTestBase
	seedTailRollups(t, repo, tier.StepHour, 1, through.Add(-2*time.Hour), through.Add(-time.Hour))
	// The 5-minute tier still holds a compacted bucket, which the hour tier
	// already counts, and two the compactor has not reached.
	seedTailRollups(t, repo, tier.Step5m, 2, through.Add(-30*time.Minute), through, through.Add(5*time.Minute))
	if err := repo.SetMetricRollupWatermark(context.Background(), tier.Step5m, through); err != nil {
		t.Fatal(err)
	}

	resp := readRange(t, srv, sm, through.Add(-47*time.Hour), through.Add(time.Hour), map[string]string{"svc": "a"})
	want := []time.Time{through.Add(-2 * time.Hour), through.Add(-time.Hour), through, through.Add(5 * time.Minute)}
	got := pointTimes(t, resp)
	if len(got) != len(want) {
		t.Fatalf("points at %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("points at %v, want %v", got, want)
		}
	}
	if resp.Step != tier.StepHour {
		t.Errorf("step_seconds = %d, want the hour tier", resp.Step)
	}
}

// A series too young to have reached the hour tier read empty on a wide
// filtered range while a narrower one showed it (#497).
func TestWideFilteredReadShowsAYoungSeries(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	seedTailRollups(t, repo, tier.Step5m, 1, rangeTestBase, rangeTestBase.Add(5*time.Minute))
	if err := repo.SetMetricRollupWatermark(context.Background(), tier.Step5m, rangeTestBase.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	resp := readRange(t, srv, sm, rangeTestBase.Add(-47*time.Hour), rangeTestBase.Add(time.Hour), map[string]string{"svc": "a"})
	if got := pointTimes(t, resp); len(got) != 2 {
		t.Errorf("points at %v, want the two 5-minute buckets", got)
	}
}

// The tail stays bounded: a watermark far behind the end of the range reads at
// most service.FineTailWidth of 5-minute buckets, so a stalled compactor cannot turn a
// filtered read back into the scan that timed out.
func TestWideFilteredReadBoundsTheTail(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	end := rangeTestBase
	old, recent := end.Add(-service.FineTailWidth-time.Hour), end.Add(-time.Hour)
	seedTailRollups(t, repo, tier.Step5m, 1, old, recent)
	if err := repo.SetMetricRollupWatermark(context.Background(), tier.Step5m, end.Add(-30*time.Hour)); err != nil {
		t.Fatal(err)
	}
	resp := readRange(t, srv, sm, end.Add(-48*time.Hour), end, map[string]string{"svc": "a"})
	if got := pointTimes(t, resp); len(got) != 1 || !got[0].Equal(recent) {
		t.Errorf("points at %v, want only %v inside the tail", got, recent)
	}
}
