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
	"github.com/wiebe-xyz/spanbarn/internal/rollup"
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
		if err := repo.UpsertMetricRollups([]repository.MetricRollup{row(rollup.Step5m)}); err != nil {
			t.Fatalf("UpsertMetricRollups: %v", err)
		}
	}
	if inHour {
		if err := repo.UpsertCoarseRollups(context.Background(), []repository.MetricRollup{row(rollup.StepHour)}); err != nil {
			t.Fatalf("UpsertCoarseRollups: %v", err)
		}
	}
}

func readRange(t *testing.T, srv *Server, sm *SessionService, from, to time.Time, labels map[string]string) metricSeriesResponse {
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
		"5m tier still holds it": {true, true, rangeTestBase.Add(-time.Hour), rangeTestBase.Add(time.Hour), rollup.Step5m},
		"only the hour tier":     {false, true, rangeTestBase.Add(-time.Hour), rangeTestBase.Add(time.Hour), rollup.StepHour},
		// The range starts after the hour bucket does and ends before the next one.
		"narrower than a bucket": {false, true, rangeTestBase.Add(10 * time.Minute), rangeTestBase.Add(50 * time.Minute), rollup.StepHour},
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
		"filtered, a day":      {filteredFineTierWidth, filter, rollup.Step5m},
		"filtered, two days":   {48 * time.Hour, filter, rollup.StepHour},
		"unfiltered, two days": {48 * time.Hour, nil, rollup.Step5m},
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
