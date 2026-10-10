package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func TestMetricTotalsPerSession(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := func(session string, off time.Duration, v float64) model.MetricRecord {
		return model.MetricRecord{
			ProjectID: 1, Name: "claude_code.cost.usage", Type: model.MetricTypeSum, Unit: "USD",
			Temporality: "delta", Value: v, TimeUnixNano: uint64(now.Add(off).UnixNano()),
			StartTimeUnixNano: uint64(now.Add(off - 10*time.Second).UnixNano()),
			Attributes:        []byte(`{"session.id":"` + session + `","model":"m"}`),
		}
	}
	insertTestMetrics(t, repo, []model.MetricRecord{
		rec("s1", 0, 0.5), rec("s1", 10*time.Second, 0.25), rec("s2", 0, 2),
	})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/metrics/totals?project_id=1&name=claude_code.cost.usage&group_by=session.id&from="+
			now.Add(-time.Hour).Format(time.RFC3339)+"&to="+now.Add(time.Hour).Format(time.RFC3339), nil)
	req.AddCookie(sessionCookie(t, sm))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp metricTotalsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Unit != "USD" || len(resp.Groups) != 2 {
		t.Fatalf("got %+v", resp)
	}
	if resp.Groups[0].Labels["session.id"] != "s2" || resp.Groups[0].Total != 2 ||
		resp.Groups[1].Labels["session.id"] != "s1" || resp.Groups[1].Total != 0.75 {
		t.Fatalf("groups %+v, want s2=2 then s1=0.75", resp.Groups)
	}
}

func TestMetricTotalsRequiresName(t *testing.T) {
	srv, sm, _ := setupMetricsQueryServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/totals?project_id=1", nil)
	req.AddCookie(sessionCookie(t, sm))
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rr.Code)
	}
}
