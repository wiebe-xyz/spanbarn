package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// One project holds production, staging and untagged spans. The environment
// parameter reads them apart and together, production also covers untagged
// spans, and a request without the parameter reads everything (#558).
func TestEnvironmentFilterOnReads(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, env string) repository.Span {
		s := healthSpan("t-"+id, id, "", "GET /x")
		s.Attributes = `{}`
		if env != "" {
			s.Attributes = `{"deployment.environment":"` + env + `"}`
		}
		return s
	}
	if err := repo.InsertSpans([]repository.Span{
		mk("p1", "production"), mk("p2", "production"), mk("s1", "staging"), mk("u1", ""),
	}); err != nil {
		t.Fatal(err)
	}

	count := func(env string) int {
		t.Helper()
		q := filterRange()
		if env != "" {
			q.Set("environment", env)
		}
		code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/spans/search?"+q.Encode(), "")
		var spans []struct{ SpanID string }
		if code != 200 || json.Unmarshal(body, &spans) != nil {
			t.Fatalf("spans/search environment=%q: %d %s", env, code, body)
		}
		return len(spans)
	}
	for env, want := range map[string]int{"": 4, "all": 4, "production": 3, "staging": 1, "testing": 0} {
		if got := count(env); got != want {
			t.Errorf("spans/search environment=%q = %d spans, want %d", env, got, want)
		}
	}

	// The same condition rides on a filter the caller supplies.
	q := filterRange()
	q.Set("environment", "staging")
	q.Set("filter", `{"filters":[{"key":"kind","op":"=","value":"server"}]}`)
	code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/spans/search?"+q.Encode(), "")
	var spans []struct{ SpanID string }
	if code != 200 || json.Unmarshal(body, &spans) != nil || len(spans) != 1 || spans[0].SpanID != "s1" {
		t.Fatalf("filter and environment: %d %s", code, body)
	}

	q = filterRange()
	q.Set("environment", "Not A Name")
	if code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/spans/search?"+q.Encode(), ""); code != 400 {
		t.Errorf("bad environment name = %d %s, want 400", code, body)
	}

	code, body = filterReq(t, srv, token, http.MethodGet, "/api/v1/environments?"+filterRange().Encode(), "")
	var rows []struct {
		Environment string
		Spans       int64
	}
	if code != 200 || json.Unmarshal(body, &rows) != nil {
		t.Fatalf("environments: %d %s", code, body)
	}
	if len(rows) != 2 || rows[0].Environment != "production" || rows[0].Spans != 3 || rows[1].Environment != "staging" || rows[1].Spans != 1 {
		t.Fatalf("environments = %s, want production 3 then staging 1", body)
	}
}

// environmentRows folds the empty group and the other row into production and
// sorts largest first, ties by name.
func TestEnvironmentRowsFolding(t *testing.T) {
	res := &service.AnalyzeResponse{
		Rows: []service.AnalyzeRow{
			{Group: []string{""}, Count: 2},
			{Group: []string{"staging"}, Count: 5},
			{Group: []string{"production"}, Count: 3},
		},
		Other: &service.AnalyzeRow{Group: []string{"testing"}, Count: 5, Other: true},
	}
	got := environmentRows(res)
	want := []environmentRow{{"production", 5}, {"staging", 5}, {"testing", 5}}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Conjoining the production condition to an OR filter with nested groups is
// refused rather than producing a filter the model rejects.
func TestWithEnvironmentRefusesTooDeepFilter(t *testing.T) {
	node, ok := filter.EnvironmentNode("production")
	if !ok || len(node.Filters) != 2 {
		t.Fatalf("production node = %+v, %v", node, ok)
	}
	if _, ok := filter.EnvironmentNode("all"); ok {
		t.Error("all must select every environment")
	}
	deep := &filter.Expr{Match: filter.MatchOr, Filters: []filter.Node{
		{Match: filter.MatchAnd, Filters: []filter.Node{{Key: "kind", Op: filter.OpEq, Value: "server"}}},
		{Key: "name", Op: filter.OpEq, Value: "x"},
	}}
	if _, ok := filter.Conjoin(deep, []filter.Node{node}); ok {
		t.Error("Conjoin accepted an OR filter with a nested group")
	}
}

// Services read per environment from aggregate rows, and a malformed name is
// refused before the read (#558 slice 2b).
func TestServicesEnvironmentScope(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	bucket := time.Now().UTC().Truncate(time.Hour)
	agg := func(env, svc string, n int64) repository.Aggregate {
		return repository.Aggregate{
			ProjectID: 1, Environment: env, Service: svc, Operation: "GET /x", Kind: "server",
			Bucket: bucket, Count: n, SumDurationUs: n * 100, MaxUs: 100,
		}
	}
	if err := repo.UpsertAggregates([]repository.Aggregate{
		agg("production", "web", 10), agg("staging", "web-staging", 3), agg("", "legacy", 2),
	}); err != nil {
		t.Fatal(err)
	}

	names := func(env string) map[string]bool {
		t.Helper()
		q := filterRange()
		q.Set("from", bucket.Add(-time.Minute).Format(time.RFC3339))
		if env != "" {
			q.Set("environment", env)
		}
		code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/services?"+q.Encode(), "")
		var rows []struct{ Service string }
		if code != 200 || json.Unmarshal(body, &rows) != nil {
			t.Fatalf("services environment=%q: %d %s", env, code, body)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.Service] = true
		}
		return out
	}
	if got := names(""); len(got) != 3 {
		t.Errorf("services without environment = %v, want all three", got)
	}
	if got := names("staging"); len(got) != 1 || !got["web-staging"] {
		t.Errorf("services environment=staging = %v, want web-staging only", got)
	}
	if got := names("production"); len(got) != 2 || !got["web"] || !got["legacy"] {
		t.Errorf("services environment=production = %v, want web and the unset legacy row", got)
	}

	q := filterRange()
	q.Set("environment", "Not A Name")
	if code, body := filterReq(t, srv, token, http.MethodGet, "/api/v1/services?"+q.Encode(), ""); code != 400 {
		t.Errorf("services bad environment = %d %s, want 400", code, body)
	}
}
