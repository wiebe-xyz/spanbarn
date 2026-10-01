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

func compareGet(t *testing.T, srv *Server, token string, q url.Values) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/attributes/compare?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

const compareSelection = `{"match":"and","filters":[{"key":"name","op":"=","value":"POST /orphan"}]}`

func TestAttributeCompareEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	var spans []repository.Span
	for i := 0; i < 8; i++ {
		name, ua := "GET /api", "curl"
		if i < 2 {
			name, ua = "POST /orphan", "Mozilla"
		}
		s := healthSpan(fmt.Sprint("t", i), fmt.Sprint("s", i), "", name)
		s.Attributes = fmt.Sprintf(`{"user_agent.original":%q}`, ua)
		spans = append(spans, s)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	q := url.Values{
		"project_id": {"1"},
		"from":       {now.Add(-time.Hour).Format(time.RFC3339)},
		"to":         {now.Add(time.Hour).Format(time.RFC3339)},
		"selection":  {compareSelection},
	}
	code, body := compareGet(t, srv, token, q)
	var res struct {
		Selection  struct{ Scanned int }
		Baseline   struct{ Scanned int }
		Attributes []struct {
			Key    string
			Score  float64
			Values []struct {
				Value          string
				SelectionShare float64
				BaselineShare  float64
			}
		}
	}
	if code != 200 || json.Unmarshal(body, &res) != nil || res.Selection.Scanned != 2 || res.Baseline.Scanned != 8 {
		t.Fatalf("compare: %d %s", code, body)
	}
	found := false
	for _, a := range res.Attributes {
		if a.Key == "user_agent.original" {
			found = true
			if a.Score < 0.7 || a.Values[0].Value != "curl" {
				t.Errorf("user agent: %+v", a)
			}
		}
	}
	if !found {
		t.Errorf("user_agent.original not ranked: %s", body)
	}

	// An explicit baseline narrows the comparison set.
	q.Set("baseline", `{"match":"and","filters":[{"key":"name","op":"=","value":"GET /api"}]}`)
	code, body = compareGet(t, srv, token, q)
	if code != 200 || json.Unmarshal(body, &res) != nil || res.Baseline.Scanned != 6 {
		t.Errorf("baseline filter: %d %s", code, body)
	}
}

func TestAttributeCompareRejectsBadRequests(t *testing.T) {
	srv, sm, _ := setupQueryTestServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	now := time.Now().UTC()
	from := now.Add(-time.Hour).Format(time.RFC3339)
	to := now.Format(time.RFC3339)

	cases := map[string]url.Values{
		"missing selection": {"project_id": {"1"}, "from": {from}, "to": {to}},
		"missing project":   {"from": {from}, "to": {to}, "selection": {compareSelection}},
		"missing range":     {"project_id": {"1"}, "selection": {compareSelection}},
		"bad selection":     {"project_id": {"1"}, "from": {from}, "to": {to}, "selection": {"{nope"}},
		"bad baseline":      {"project_id": {"1"}, "from": {from}, "to": {to}, "selection": {compareSelection}, "baseline": {"[1]"}},
		"bad operator": {"project_id": {"1"}, "from": {from}, "to": {to},
			"selection": {`{"match":"and","filters":[{"key":"a","op":"~","value":"b"}]}`}},
	}
	for name, q := range cases {
		if code, body := compareGet(t, srv, token, q); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, code, body)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/attributes/compare?project_id=1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", rec.Code)
	}
}
