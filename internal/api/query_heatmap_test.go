package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func heatmapGet(t *testing.T, srv *Server, token string, q url.Values) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/heatmap?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestHeatmapEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var spans []repository.Span
	for i := 0; i < 6; i++ {
		name, dur := "GET /api", int64(1000)
		if i < 2 {
			name, dur = "POST /slow", 900_000
		}
		s := healthSpan(fmt.Sprint("t", i), fmt.Sprint("s", i), "", name)
		s.StartTimeUs, s.DurationUs = now.UnixMicro(), dur
		spans = append(spans, s)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	q := url.Values{
		"project_id":       {"1"},
		"from":             {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":               {now.Add(time.Hour).Format(time.RFC3339)},
		"time_buckets":     {"4"},
		"duration_buckets": {"5"},
	}
	var res struct {
		Scanned         int64
		Capped          bool
		TimeBuckets     int
		DurationBuckets int
		DurationEdgesUs []int64
		Cells           []struct {
			Time, Duration int
			Count          int64
		}
	}
	code, body := heatmapGet(t, srv, token, q)
	if code != 200 || json.Unmarshal(body, &res) != nil || res.Scanned != 6 || res.TimeBuckets != 4 || len(res.DurationEdgesUs) != 6 {
		t.Fatalf("heatmap: %d %s", code, body)
	}
	var total int64
	for _, c := range res.Cells {
		total += c.Count
	}
	if total != 6 || res.Capped {
		t.Errorf("cells hold %d spans, capped=%v", total, res.Capped)
	}

	q.Set("filter", `{"match":"and","filters":[{"key":"name","op":"=","value":"POST /slow"}]}`)
	q.Set("max_spans", "1")
	code, body = heatmapGet(t, srv, token, q)
	if code != 200 || json.Unmarshal(body, &res) != nil || res.Scanned != 1 || !res.Capped {
		t.Errorf("filtered and capped: %d %s", code, body)
	}
}

func TestHeatmapRejectsBadRequests(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	cases := map[string]url.Values{
		"missing project": {"from": {from}, "to": {to}},
		"missing range":   {"project_id": {"1"}},
		"bad filter":      {"project_id": {"1"}, "from": {from}, "to": {to}, "filter": {"{nope"}},
		"bad operator": {"project_id": {"1"}, "from": {from}, "to": {to},
			"filter": {`{"match":"and","filters":[{"key":"a","op":"~","value":"b"}]}`}},
		"range over 7 days": {"project_id": {"1"}, "from": {now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}, "to": {to}},
	}
	for name, q := range cases {
		if code, body := heatmapGet(t, srv, token, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, code, body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/heatmap?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", rec.Code)
	}
}
