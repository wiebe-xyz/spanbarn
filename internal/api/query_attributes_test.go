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

func attributeGet(t *testing.T, srv *Server, token string, q url.Values) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attributes?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestAttributesEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	span := healthSpan("t1", "a", "", "POST /api/v1/library/presign")
	span.Attributes = `{"client.address":"10.0.0.1","user_agent.original":"curl","http.response.status_code":200}`
	if err := repo.InsertSpans([]repository.Span{span}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	q := url.Values{
		"project_id": {"1"},
		"span_name":  {"POST /api/v1/library/presign"},
		"from":       {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":         {now.Add(time.Hour).Format(time.RFC3339)},
	}
	code, body := attributeGet(t, srv, token, q)
	var res struct {
		Scanned int
		Sample  int
		Keys    []struct {
			Key      string
			Coverage float64
			Distinct int
			Top      []struct {
				Value string
				Count int
			}
		}
	}
	if code != 200 || json.Unmarshal(body, &res) != nil || res.Scanned != 1 || res.Sample != 1 || len(res.Keys) != 3 {
		t.Fatalf("attributes: %d %s", code, body)
	}
	found := map[string]string{}
	for _, k := range res.Keys {
		found[k.Key] = k.Top[0].Value
		if k.Coverage != 1 || k.Distinct != 1 {
			t.Errorf("%s coverage/distinct = %v/%d", k.Key, k.Coverage, k.Distinct)
		}
	}
	for key, want := range map[string]string{"client.address": "10.0.0.1", "user_agent.original": "curl", "http.response.status_code": "200"} {
		if found[key] != want {
			t.Errorf("%s top value = %q, want %q", key, found[key], want)
		}
	}

	q.Set("key", "client.address")
	code, body = attributeGet(t, srv, token, q)
	if code != 200 || json.Unmarshal(body, &res) != nil || len(res.Keys) != 1 {
		t.Errorf("single key: %d %s", code, body)
	}
}

func TestAttributesRequireBoundedRange(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	cases := map[string]url.Values{
		"missing project":    {"from": {from}, "to": {to}},
		"missing range":      {"project_id": {"1"}},
		"missing from":       {"project_id": {"1"}, "to": {to}},
		"reversed range":     {"project_id": {"1"}, "from": {to}, "to": {from}},
		"range over 7 days":  {"project_id": {"1"}, "from": {now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)}, "to": {to}},
		"unparseable window": {"project_id": {"1"}, "from": {"yesterday"}, "to": {"today"}},
		"sample too large":   {"project_id": {"1"}, "from": {from}, "to": {to}, "sample": {"100000"}},
	}
	for name, q := range cases {
		if code, body := attributeGet(t, srv, token, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, code, body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attributes?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", rec.Code)
	}
}
