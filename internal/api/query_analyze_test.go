package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestAnalyzeEndpoints(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, kind, path string, dur int64) repository.Span {
		s := healthSpan("t-"+id, id, "", "GET "+path)
		s.Kind = kind
		s.DurationUs = dur
		s.Attributes = `{"url.path":"` + path + `"}`
		return s
	}
	if err := repo.InsertSpans([]repository.Span{
		mk("a1", "server", "/a", 100), mk("a2", "server", "/a", 300),
		mk("b1", "server", "/b", 50), mk("c1", "client", "/a", 9),
	}); err != nil {
		t.Fatal(err)
	}

	q := filterRange()
	q.Set("filter", `{"filters":[{"key":"kind","op":"=","value":"server"}]}`)
	q["group_by"] = []string{"url.path"}
	q["calc"] = []string{"count", "p95"}
	code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/analyze?"+q.Encode(), "")
	var table struct {
		Calcs []string
		Rows  []struct {
			Group  []string
			Count  int64
			Values []float64
			Drill  json.RawMessage
		}
	}
	if code != 200 || json.Unmarshal(body, &table) != nil {
		t.Fatalf("analyze: %d %s", code, body)
	}
	if len(table.Rows) != 2 || table.Rows[0].Group[0] != "/a" || table.Rows[0].Values[0] != 2 || table.Rows[0].Values[1] != 300 {
		t.Fatalf("rows = %s", body)
	}
	var drill struct {
		Filters []struct{ Key, Value string }
	}
	if json.Unmarshal(table.Rows[0].Drill, &drill) != nil || len(drill.Filters) != 2 ||
		drill.Filters[1].Key != "url.path" || drill.Filters[1].Value != "/a" {
		t.Fatalf("drill = %s", table.Rows[0].Drill)
	}

	// The drill filter returns exactly the spans of the row.
	d := filterRange()
	d.Set("filter", string(table.Rows[0].Drill))
	code, body = filterReq(t, srv, token, http.MethodGet, "/api/v1/spans/search?"+d.Encode(), "")
	var spans []struct{ SpanID string }
	if code != 200 || json.Unmarshal(body, &spans) != nil || len(spans) != 2 {
		t.Fatalf("drill spans: %d %s", code, body)
	}

	q.Set("calc", "p95")
	code, body = filterReq(t, srv, token, http.MethodGet, "/api/v1/analyze/series?"+q.Encode(), "")
	var series struct {
		Calc    string
		Buckets []int64
		Series  []struct {
			Group  []string
			Values []*float64
		}
	}
	if code != 200 || json.Unmarshal(body, &series) != nil || series.Calc != "p95" || len(series.Series) != 2 || len(series.Buckets) == 0 {
		t.Fatalf("series: %d %s", code, body)
	}
}

func TestAnalyzeBadRequests(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]url.Values{
		"unknown calc":  {"calc": {"median"}},
		"distinct key":  {"calc": {"count_distinct"}},
		"two series":    {"calc": {"count", "p95"}},
		"order_by":      {"calc": {"count"}, "order_by": {"p95"}},
		"too many keys": {"group_by": {"a", "b", "c", "d", "e"}},
		"bad filter":    {"filter": {`{"filters":[{"key":"a","op":"~"}]}`}},
	}
	for name, extra := range cases {
		q := filterRange()
		for k, v := range extra {
			q[k] = v
		}
		path := "/api/v1/analyze?"
		if name == "two series" {
			path = "/api/v1/analyze/series?"
		}
		if code, body := filterReq(t, srv, token, http.MethodGet, path+q.Encode(), ""); code != 400 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}

	noRange := url.Values{"project_id": {"1"}}
	if code, _ := filterReq(t, srv, token, http.MethodGet, "/api/v1/analyze?"+noRange.Encode(), ""); code != 400 {
		t.Errorf("without a time range: %d", code)
	}
	wide := filterRange()
	wide.Set("from", "2000-01-01T00:00:00Z")
	if code, _ := filterReq(t, srv, token, http.MethodGet, "/api/v1/analyze?"+wide.Encode(), ""); code != 400 {
		t.Errorf("range over the cap: %d", code)
	}
	noProject := filterRange()
	noProject.Del("project_id")
	if code, _ := filterReq(t, srv, token, http.MethodGet, "/api/v1/analyze?"+noProject.Encode(), ""); code != 400 {
		t.Errorf("without a project: %d", code)
	}
}
