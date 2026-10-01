package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func healthSpan(trace, span, parent, name string) repository.Span {
	return repository.Span{
		ProjectID: 1, TraceID: trace, SpanID: span, ParentSpanID: parent,
		Name: name, Service: "web", Kind: "server", Status: "ok",
		StartTimeUs: 1, DurationUs: 1, Attributes: "{}", Events: "[]",
	}
}

func TestTraceHealthEndpoints(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSpans([]repository.Span{
		healthSpan("t1", "a", "", "job.run"),
		healthSpan("t2", "b", "ghost", "POST /presign"),
		healthSpan("t3", "c", "", "GET /x"),
		healthSpan("t3", "d", "c", "SELECT"),
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	get := func(path string, q url.Values) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path+"?"+q.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: "session", Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	window := url.Values{
		"project_id": {"1"},
		"from":       {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":         {now.Add(time.Hour).Format(time.RFC3339)},
	}

	var orphans []map[string]any
	code, body := get("/api/v1/trace-health/orphan-spans", window)
	if code != 200 || json.Unmarshal(body, &orphans) != nil || len(orphans) != 1 || orphans[0]["name"] != "POST /presign" {
		t.Fatalf("orphan-spans: %d %s", code, body)
	}

	var rootless struct {
		Total  int
		Traces []map[string]any
	}
	code, body = get("/api/v1/trace-health/rootless-traces", window)
	if code != 200 || json.Unmarshal(body, &rootless) != nil || rootless.Total != 1 || len(rootless.Traces) != 1 {
		t.Fatalf("rootless-traces: %d %s", code, body)
	}
	if rootless.Traces[0]["hasRoot"] != false || rootless.Traces[0]["rootSpanName"] != "" {
		t.Errorf("rootless trace should report hasRoot=false and no root name: %v", rootless.Traces[0])
	}

	var single []map[string]any
	code, body = get("/api/v1/trace-health/single-span-traces", window)
	if code != 200 || json.Unmarshal(body, &single) != nil || len(single) != 2 {
		t.Fatalf("single-span-traces: %d %s", code, body)
	}

	var names []map[string]any
	code, body = get("/api/v1/trace-health/span-names", window)
	if code != 200 || json.Unmarshal(body, &names) != nil || len(names) != 4 {
		t.Fatalf("span-names: %d %s", code, body)
	}

	// The trace list filters on has_root and orphans.
	var traces []map[string]any
	list := url.Values{"project_id": {"1"}, "from": window["from"], "to": window["to"], "has_root": {"false"}}
	code, body = get("/api/v1/traces", list)
	if code != 200 || json.Unmarshal(body, &traces) != nil || len(traces) != 1 || traces[0]["traceId"] != "t2" {
		t.Fatalf("has_root=false: %d %s", code, body)
	}
	list = url.Values{"project_id": {"1"}, "from": window["from"], "to": window["to"], "orphans": {"true"}}
	code, body = get("/api/v1/traces", list)
	if code != 200 || json.Unmarshal(body, &traces) != nil || len(traces) != 1 || traces[0]["orphanCount"] != float64(1) {
		t.Fatalf("orphans=true: %d %s", code, body)
	}
}

func TestTraceHealthRequiresScopeAndBoundedRange(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	now := time.Now().UTC()

	cases := map[string]url.Values{
		"missing project":    {"from": {now.Add(-time.Hour).Format(time.RFC3339)}, "to": {now.Format(time.RFC3339)}},
		"missing range":      {"project_id": {"1"}},
		"missing from":       {"project_id": {"1"}, "to": {now.Format(time.RFC3339)}},
		"reversed range":     {"project_id": {"1"}, "from": {now.Format(time.RFC3339)}, "to": {now.Add(-time.Hour).Format(time.RFC3339)}},
		"range over 7 days":  {"project_id": {"1"}, "from": {now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)}, "to": {now.Format(time.RFC3339)}},
		"unparseable window": {"project_id": {"1"}, "from": {"yesterday"}, "to": {"today"}},
	}
	for name, q := range cases {
		for _, path := range []string{"orphan-spans", "rootless-traces", "single-span-traces", "span-names"} {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/trace-health/"+path+"?"+q.Encode(), nil)
			req.AddCookie(&http.Cookie{Name: "session", Value: token})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s %s: status %d, want 400", name, path, rec.Code)
			}
		}
	}
}
