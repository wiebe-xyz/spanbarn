package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const libraryFilter = `{"filters":[` +
	`{"key":"kind","op":"=","value":"server"},` +
	`{"key":"url.path","op":"starts-with","value":"/api/v1/library"},` +
	`{"key":"http.response.status_code","op":">=","value":500}]}`

func filterReq(t *testing.T, srv *Server, token, method, path string, body string) (int, []byte) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func filterRange() url.Values {
	now := time.Now().UTC()
	return url.Values{
		"project_id": {"1"},
		"from":       {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":         {now.Add(time.Hour).Format(time.RFC3339)},
	}
}

func TestFilterModelEndpoints(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	hit := healthSpan("t1", "a", "", "GET /api/v1/library/books")
	hit.Attributes = `{"url.path":"/api/v1/library/books","http.response.status_code":502}`
	miss := healthSpan("t2", "b", "", "GET /api/v1/library/books")
	miss.Attributes = `{"url.path":"/api/v1/library/books","http.response.status_code":200}`
	if err := repo.InsertSpans([]repository.Span{hit, miss}); err != nil {
		t.Fatal(err)
	}

	q := filterRange()
	q.Set("filter", libraryFilter)

	code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/spans/search?"+q.Encode(), "")
	var spans []struct{ TraceID, SpanID string }
	if code != 200 || json.Unmarshal(body, &spans) != nil || len(spans) != 1 || spans[0].SpanID != "a" {
		t.Fatalf("spans/search: %d %s", code, body)
	}

	code, body = filterReq(t, srv, token, http.MethodGet, "/api/v1/traces?"+q.Encode(), "")
	var traces []struct{ TraceID string }
	if code != 200 || json.Unmarshal(body, &traces) != nil || len(traces) != 1 || traces[0].TraceID != "t1" {
		t.Fatalf("traces: %d %s", code, body)
	}
}

func TestFilterModelBadRequests(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}

	bad := filterRange()
	bad.Set("filter", `{"filters":[{"key":"a","op":"~","value":"x"}]}`)
	for _, path := range []string{"/api/v1/spans/search?", "/api/v1/traces?"} {
		if code, body := filterReq(t, srv, token, http.MethodGet, path+bad.Encode(), ""); code != 400 {
			t.Errorf("%s bad operator: %d %s", path, code, body)
		}
	}

	// A filter without a time range would scan every span.
	noRange := url.Values{"project_id": {"1"}, "filter": {libraryFilter}}
	for _, path := range []string{"/api/v1/spans/search?", "/api/v1/traces?"} {
		if code, body := filterReq(t, srv, token, http.MethodGet, path+noRange.Encode(), ""); code != 400 {
			t.Errorf("%s without from: %d %s", path, code, body)
		}
	}
}

func TestSavedQueriesCarryFilters(t *testing.T) {
	_, sm, repo := setupQueryTestServer(t)
	srv := NewServerWithQuery(ServerConfig{APIKey: "test-key", Version: "test"}, nil, nil, sm, nil, WithRepository(repo))
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateProject("p1", "P1"); err != nil {
		t.Fatal(err)
	}

	// New form.
	code, body := filterReq(t, srv, token, http.MethodPost, "/api/v1/saved-queries",
		`{"name":"lib","projectId":1,"filters":`+libraryFilter+`}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	// Legacy form maps to filters.
	code, body = filterReq(t, srv, token, http.MethodPost, "/api/v1/saved-queries",
		`{"name":"old","projectId":1,"service":"web","minDurationUs":1500}`)
	if code != http.StatusCreated {
		t.Fatalf("create legacy: %d %s", code, body)
	}
	// Invalid filters are refused.
	code, _ = filterReq(t, srv, token, http.MethodPost, "/api/v1/saved-queries",
		`{"name":"bad","projectId":1,"filters":{"filters":[{"key":"a","op":"~"}]}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("invalid filters: %d", code)
	}

	code, body = filterReq(t, srv, token, http.MethodGet, "/api/v1/saved-queries?project_id=1", "")
	var qs []struct {
		Name    string
		Filters json.RawMessage
	}
	if code != 200 || json.Unmarshal(body, &qs) != nil || len(qs) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	for _, q := range qs {
		want := `"duration_us"`
		if q.Name == "lib" {
			want = `"starts-with"`
		}
		if !strings.Contains(string(q.Filters), want) {
			t.Errorf("%s filters = %s, want it to contain %s", q.Name, q.Filters, want)
		}
	}
}
