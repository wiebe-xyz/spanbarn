package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/session"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
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

// totalsServerWithCache is setupMetricsQueryServer with a read cache, as in
// production.
func totalsServerWithCache(t *testing.T) (*Server, *session.Service, *repository.Repository) {
	t.Helper()
	_, sm, repo := setupMetricsQueryServer(t)
	srv := NewServerWithQuery(ServerConfig{APIKey: "test-key", Version: "test"}, nil, nil, sm, nil,
		WithRepository(repo), WithCache(cache.New(cache.NewMemoryStore(), time.Minute)))
	return srv, sm, repo
}

func totalsFor(t *testing.T, srv *Server, sm *session.Service, url string) metricTotalsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
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
	return resp
}

// TestMetricTotalsAreCachedPerWindow: a second identical read is answered from
// the cache, so a page view that asks for several metrics and a reload do not
// each scan the tier again. The proof is that a point written between the two
// reads shows up only once the cached window is out of date.
func TestMetricTotalsAreCachedPerWindow(t *testing.T) {
	srv, sm, repo := totalsServerWithCache(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := func(off time.Duration, v float64) model.MetricRecord {
		return model.MetricRecord{
			ProjectID: 1, Name: "claude_code.cost.usage", Type: model.MetricTypeSum, Unit: "USD",
			Temporality: "delta", Value: v, TimeUnixNano: uint64(now.Add(off).UnixNano()),
			StartTimeUnixNano: uint64(now.Add(off - 10*time.Second).UnixNano()),
			Attributes:        []byte(`{"session.id":"s1"}`),
		}
	}
	insertTestMetrics(t, repo, []model.MetricRecord{rec(0, 0.5)})
	url := "/api/v1/metrics/totals?project_id=1&name=claude_code.cost.usage&group_by=session.id&from=" +
		now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339)

	first := totalsFor(t, srv, sm, url)
	insertTestMetrics(t, repo, []model.MetricRecord{rec(5*time.Second, 4)})
	second := totalsFor(t, srv, sm, url)

	if len(first.Groups) != 1 || first.Groups[0].Total != 0.5 {
		t.Fatalf("first read %+v, want s1=0.5", first.Groups)
	}
	if len(second.Groups) != 1 || second.Groups[0].Total != 0.5 {
		t.Fatalf("second read %+v, want the cached s1=0.5", second.Groups)
	}
}

// TestMetricTotalsCacheKeepsGroupsApart: another group_by or label filter is
// another entry.
func TestMetricTotalsCacheKeepsGroupsApart(t *testing.T) {
	srv, sm, repo := totalsServerWithCache(t)
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(session, model_ string, v float64) model.MetricRecord {
		return model.MetricRecord{
			ProjectID: 1, Name: "claude_code.cost.usage", Type: model.MetricTypeSum, Unit: "USD",
			Temporality: "delta", Value: v, TimeUnixNano: uint64(now.UnixNano()),
			StartTimeUnixNano: uint64(now.Add(-10 * time.Second).UnixNano()),
			Attributes:        []byte(`{"session.id":"` + session + `","model":"` + model_ + `"}`),
		}
	}
	insertTestMetrics(t, repo, []model.MetricRecord{mk("s1", "a", 1), mk("s2", "b", 2)})
	base := "/api/v1/metrics/totals?project_id=1&name=claude_code.cost.usage&from=" +
		now.Add(-time.Hour).Format(time.RFC3339) + "&to=" + now.Add(time.Hour).Format(time.RFC3339)

	bySession := totalsFor(t, srv, sm, base+"&group_by=session.id")
	byModel := totalsFor(t, srv, sm, base+"&group_by=model")
	onlyA := totalsFor(t, srv, sm, base+"&group_by=session.id&label[model]=a")

	if len(bySession.Groups) != 2 || bySession.Groups[0].Labels["session.id"] == "" {
		t.Fatalf("by session %+v", bySession.Groups)
	}
	if len(byModel.Groups) != 2 || byModel.Groups[0].Labels["model"] == "" {
		t.Fatalf("by model %+v", byModel.Groups)
	}
	if len(onlyA.Groups) != 1 || onlyA.Groups[0].Total != 1 {
		t.Fatalf("filtered %+v, want one group with 1", onlyA.Groups)
	}
}

// seedDeltaTotals stores delta sum buckets of value v in one tier.
func seedDeltaTotals(t *testing.T, repo *repository.Repository, step int64, v float64, at ...time.Time) {
	t.Helper()
	rows := make([]repository.MetricRollup, 0, len(at))
	for _, b := range at {
		rows = append(rows, repository.MetricRollup{
			ProjectID: 1, Name: "t", Type: "sum", Unit: "USD", Temporality: "delta",
			AttrFingerprint: "a", Attributes: `{"svc":"a"}`, Bucket: b, StepSeconds: step, Count: 1, Sum: v, Last: v,
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

// A day or more of totals reads the hour tier plus the 5-minute buckets the
// compactor has not reached, instead of every 5-minute bucket of the range
// (#457). The 5-minute tier still holds the compacted buckets, so reading it
// would count them as well.
func TestWideTotalsReadTheHourTier(t *testing.T) {
	srv, sm, repo := setupMetricsQueryServer(t)
	through := rangeTestBase
	hours := []time.Time{through.Add(-3 * time.Hour), through.Add(-2 * time.Hour), through.Add(-time.Hour)}
	seedDeltaTotals(t, repo, tier.StepHour, 10, hours...)
	var fine []time.Time
	for b := through.Add(-3 * time.Hour); !b.After(through.Add(5 * time.Minute)); b = b.Add(5 * time.Minute) {
		fine = append(fine, b)
	}
	seedDeltaTotals(t, repo, tier.Step5m, 1, fine...)
	if err := repo.SetMetricRollupWatermark(context.Background(), tier.Step5m, through); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		width time.Duration
		want  float64
	}{
		"36 hours reads hours plus the tail": {36 * time.Hour, 32},
		"12 hours keeps the 5-minute tier":   {12 * time.Hour, 38},
	} {
		t.Run(name, func(t *testing.T) {
			url := "/api/v1/metrics/totals?name=t&project_id=1&from=" + through.Add(time.Hour-tc.width).Format(time.RFC3339) +
				"&to=" + through.Add(time.Hour).Format(time.RFC3339)
			resp := totalsFor(t, srv, sm, url)
			if len(resp.Groups) != 1 || resp.Groups[0].Total != tc.want {
				t.Fatalf("groups %+v, want one group of %v", resp.Groups, tc.want)
			}
		})
	}
}
