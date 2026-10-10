package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func querySeries(t *testing.T, srv *Server, sm *SessionService, name string, from, to time.Time) metricSeriesResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/series?project_id=1&name="+name+"&from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339), nil)
	req.AddCookie(sessionCookie(t, sm))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp metricSeriesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// A delta sum stores the increase per export. Charted as a cumulative counter
// it showed the difference between consecutive increases, which is noise and
// goes negative.
func TestMetricSeriesRawDeltaSumRate(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := func(offset time.Duration, v float64) model.MetricRecord {
		ts := now.Add(offset)
		return model.MetricRecord{
			ProjectID: 1, Name: "claude_code.cost.usage", Type: model.MetricTypeSum,
			Temporality: "delta", Value: v,
			StartTimeUnixNano: uint64(ts.Add(-10 * time.Second).UnixNano()),
			TimeUnixNano:      uint64(ts.UnixNano()),
			Attributes:        []byte(`{"session.id":"s1"}`),
		}
	}
	insertTestMetrics(t, repo, []model.MetricRecord{rec(0, 100), rec(10*time.Second, 5), rec(20*time.Second, 150)})

	resp := querySeries(t, srv, sm, "claude_code.cost.usage", now.Add(-time.Hour), now.Add(time.Hour))
	if resp.Render != "rate" || len(resp.Series) != 1 {
		t.Fatalf("render %q, %d series", resp.Render, len(resp.Series))
	}
	pts := resp.Series[0].Points
	want := []float64{10, 0.5, 15}
	if len(pts) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(pts), len(want), pts)
	}
	for i, w := range want {
		if pts[i].Value != w {
			t.Errorf("point %d = %v, want %v", i, pts[i].Value, w)
		}
	}
}

func TestMetricSeriesRollupDeltaSumRate(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	base := time.Date(2026, 6, 16, 6, 0, 0, 0, time.UTC)
	row := func(at time.Time, sum, last float64) repository.MetricRollup {
		return repository.MetricRollup{
			ProjectID: 1, Name: "claude_code.token.usage", Type: "sum", Temporality: "delta",
			AttrFingerprint: "fp", Attributes: `{"host":"h1"}`, Bucket: at,
			Count: 3, Sum: sum, Min: 1, Max: last, Last: last,
		}
	}
	if err := repo.UpsertMetricRollups([]repository.MetricRollup{
		row(base, 3000, 1000), row(base.Add(5*time.Minute), 600, 200),
	}); err != nil {
		t.Fatalf("seed rollups: %v", err)
	}

	resp := querySeries(t, srv, sm, "claude_code.token.usage", base.Add(-time.Hour), base.Add(8*time.Hour))
	pts := resp.Series[0].Points
	// 3000 over a 300s bucket is 10/s; 600 is 2/s.
	if len(pts) != 2 || pts[0].Value != 10 || pts[1].Value != 2 {
		t.Fatalf("got %+v, want rates 10 and 2", pts)
	}
}
