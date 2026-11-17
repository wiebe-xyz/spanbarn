package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// Metric reads take the environment parameter: production also reads series
// without the label, a name reads its own series, a missing parameter and "all"
// read everything and a malformed name is a 400 (#558).
func TestMetricSeriesEnvironmentScope(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := func(offset time.Duration, attrs string) model.MetricRecord {
		return model.MetricRecord{
			ProjectID: 1, Name: "env.gauge", Type: model.MetricTypeGauge, Value: 1,
			TimeUnixNano: uint64(now.Add(offset).UnixNano()), Attributes: []byte(attrs),
		}
	}
	insertTestMetrics(t, repo, []model.MetricRecord{
		rec(0, `{"deployment.environment":"production"}`),
		rec(time.Second, `{"deployment.environment":"staging"}`),
		rec(2*time.Second, `{"host":"legacy"}`),
	})

	points := func(env string) int {
		t.Helper()
		url := "/api/v1/metrics/series?project_id=1&name=env.gauge&from=" + now.Add(-time.Hour).Format(time.RFC3339) +
			"&to=" + now.Add(time.Hour).Format(time.RFC3339)
		if env != "" {
			url += "&environment=" + neturl.QueryEscape(env)
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.AddCookie(sessionCookie(t, sm))
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if env == "Not A Name" || w.Code != http.StatusOK {
			return w.Code
		}
		return decodeSeriesPoints(t, w)
	}
	for env, want := range map[string]int{"": 3, "all": 3, "production": 2, "staging": 1, "testing": 0} {
		if got := points(env); got != want {
			t.Errorf("environment %q = %d points, want %d", env, got, want)
		}
	}
	if got := points("Not A Name"); got != http.StatusBadRequest {
		t.Errorf("bad environment = %d, want 400", got)
	}
}

func decodeSeriesPoints(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	var resp metricSeriesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	n := 0
	for _, s := range resp.Series {
		n += len(s.Points)
	}
	return n
}
