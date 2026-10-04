package repository

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func filterSpan(trace, span, name, service, kind, attrs string, dur int64) Span {
	return Span{
		ProjectID: 1, TraceID: trace, SpanID: span, Name: name, Service: service,
		Kind: kind, Status: "ok", StartTimeUs: 1000, DurationUs: dur,
		Attributes: attrs, Events: "[]",
	}
}

func seedFilterSpans(t *testing.T) *Repository {
	t.Helper()
	repo := setupTestDB(t)
	if err := repo.InsertSpans([]Span{
		filterSpan("t1", "s1", "GET /api/v1/library/books", "web", "server",
			`{"url.path":"/api/v1/library/books","http.response.status_code":500,"cache":true,"user":"ann"}`, 9000),
		filterSpan("t1", "s2", "SELECT", "db", "client", `{"db.system":"sqlite"}`, 300),
		filterSpan("t2", "s3", "GET /api/v1/library/shelves", "web", "server",
			`{"url.path":"/api/v1/library/shelves","http.response.status_code":200,"cache":false}`, 100),
		filterSpan("t3", "s4", "GET /health", "web", "server",
			`{"url.path":"/health","http.response.status_code":503,"user":"bob"}`, 50),
		filterSpan("t4", "s5", "GET /broken", "web", "server", `not json`, 70),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return repo
}

func spanIDsFor(t *testing.T, repo *Repository, raw string) []string {
	t.Helper()
	expr, err := filter.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spans, err := repo.QuerySpans(SpanFilter{
		ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour), Limit: 100,
	})
	if err != nil {
		t.Fatalf("QuerySpans: %v", err)
	}
	ids := make([]string, 0, len(spans))
	for _, s := range spans {
		ids = append(ids, s.SpanID)
	}
	sort.Strings(ids)
	return ids
}

func TestQuerySpansFilterModel(t *testing.T) {
	repo := seedFilterSpans(t)
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"issue acceptance: kind AND path prefix AND status >= 500",
			`{"filters":[{"key":"kind","op":"=","value":"server"},{"key":"url.path","op":"starts-with","value":"/api/v1/library"},{"key":"http.response.status_code","op":">=","value":500}]}`,
			[]string{"s1"}},
		{"OR across two attributes",
			`{"match":"or","filters":[{"key":"user","op":"=","value":"ann"},{"key":"user","op":"=","value":"bob"}]}`,
			[]string{"s1", "s4"}},
		{"one level of grouping",
			`{"filters":[{"key":"kind","op":"=","value":"server"},{"match":"or","filters":[{"key":"user","op":"=","value":"bob"},{"key":"url.path","op":"contains","value":"shelves"}]}]}`,
			[]string{"s3", "s4"}},
		{"numeric > on attribute", `{"filters":[{"key":"http.response.status_code","op":">","value":500}]}`, []string{"s4"}},
		{"numeric < on attribute", `{"filters":[{"key":"http.response.status_code","op":"<","value":300}]}`, []string{"s3"}},
		{"boolean attribute", `{"filters":[{"key":"cache","op":"=","value":"true"}]}`, []string{"s1"}},
		{"!= matches a missing attribute", `{"filters":[{"key":"user","op":"!=","value":"ann"}]}`, []string{"s2", "s3", "s4", "s5"}},
		{"exists", `{"filters":[{"key":"user","op":"exists"}]}`, []string{"s1", "s4"}},
		{"does-not-exist", `{"filters":[{"key":"user","op":"does-not-exist"}]}`, []string{"s2", "s3", "s5"}},
		{"in", `{"filters":[{"key":"url.path","op":"in","values":["/health","/api/v1/library/books"]}]}`, []string{"s1", "s4"}},
		{"not-in", `{"filters":[{"key":"service","op":"not-in","values":["web"]}]}`, []string{"s2"}},
		{"column duration >=", `{"filters":[{"key":"duration_us","op":">=","value":300}]}`, []string{"s1", "s2"}},
		{"column alias operation", `{"filters":[{"key":"operation","op":"=","value":"SELECT"}]}`, []string{"s2"}},
		{"generated http_status column", `{"filters":[{"key":"http_status","op":"=","value":503}]}`, []string{"s4"}},
		{"attributes. prefix reads the attribute", `{"filters":[{"key":"attributes.db.system","op":"=","value":"sqlite"}]}`, []string{"s2"}},
		{"contains is case sensitive", `{"filters":[{"key":"name","op":"contains","value":"get"}]}`, nil},
		{"malformed attributes JSON never errors", `{"filters":[{"key":"url.path","op":"exists"}]}`, []string{"s1", "s3", "s4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spanIDsFor(t, repo, tc.raw)
			if len(got) == 0 {
				got = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterRequiresTimeRange(t *testing.T) {
	repo := seedFilterSpans(t)
	expr, _ := filter.Parse(`{"filters":[{"key":"user","op":"exists"}]}`)
	if _, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Expr: expr}); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("QuerySpans err = %v, want ErrInvalid", err)
	}
	if _, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, Expr: expr}, 0); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("SearchTraceSummaries err = %v, want ErrInvalid", err)
	}
}

// A trace matches when one of its spans satisfies the whole expression.
func TestSearchTraceSummariesFilterModel(t *testing.T) {
	repo := seedFilterSpans(t)
	expr, err := filter.Parse(`{"filters":[{"key":"http.response.status_code","op":">=","value":500}]}`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := repo.SearchTraceSummaries(SpanFilter{
		ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour), Limit: 50,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.TraceID)
	}
	sort.Strings(ids)
	if want := []string{"t1", "t3"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("traces = %v, want %v", ids, want)
	}
	// t1 keeps its summary of both spans, not only the matching one.
	for _, r := range rows {
		if r.TraceID == "t1" && r.SpanCount != 2 {
			t.Fatalf("t1 span count = %d, want 2", r.SpanCount)
		}
	}
}

func TestSavedQueryFiltersRoundTrip(t *testing.T) {
	repo := setupTestDB(t)
	if _, err := repo.CreateProject("p1", "P1"); err != nil {
		t.Fatal(err)
	}
	raw := `{"match":"and","filters":[{"key":"service","op":"=","value":"web"}]}`
	if _, err := repo.CreateSavedQuery(SavedQuery{ProjectID: 1, Name: "web", Filters: []byte(raw)}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSavedQuery(SavedQuery{ProjectID: 1, Name: "empty"}); err != nil {
		t.Fatal(err)
	}
	qs, err := repo.ListSavedQueries(1)
	if err != nil || len(qs) != 2 {
		t.Fatalf("list: %v %v", qs, err)
	}
	for _, q := range qs {
		switch q.Name {
		case "web":
			if string(q.Filters) != raw {
				t.Fatalf("filters = %s, want %s", q.Filters, raw)
			}
		case "empty":
			if q.Filters != nil {
				t.Fatalf("empty query filters = %s, want nil", q.Filters)
			}
		}
	}
}

// Migration 036 maps the four legacy saved-query fields onto the filter model.
func TestMigration036BackfillsSavedQueryFilters(t *testing.T) {
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db.DB, ".", 35); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO projects (id, slug, name) VALUES (1, 'p', 'P')`,
		`INSERT INTO saved_queries (project_id, name, service, operation, status, min_duration_us) VALUES (1, 'all', 'web', 'GET /x', 'error', 2500)`,
		`INSERT INTO saved_queries (project_id, name) VALUES (1, 'none')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := goose.Up(db.DB, "."); err != nil {
		t.Fatal(err)
	}
	qs, err := NewRepository(db.DB).ListSavedQueries(1)
	if err != nil || len(qs) != 2 {
		t.Fatalf("list: %v %v", qs, err)
	}
	for _, q := range qs {
		if q.Name == "none" {
			if q.Filters != nil {
				t.Fatalf("query without fields got filters %s", q.Filters)
			}
			continue
		}
		expr, err := filter.Parse(string(q.Filters))
		if err != nil || expr == nil {
			t.Fatalf("migrated filters %s: %v", q.Filters, err)
		}
		if got, want := filter.Marshal(expr), filter.Marshal(filter.FromLegacy("web", "GET /x", "error", 2500)); got != want {
			t.Fatalf("migrated %s, want %s", got, want)
		}
	}
}
