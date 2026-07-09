package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func dbTestSpans(now time.Time) []repository.Span {
	// Parent server span (caller context) + client DB spans. Two SELECTs share a
	// normalized pattern (different literals); one INSERT is a separate pattern.
	return []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "p1", Name: "GET /orders", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 900,
			Attributes: "{}", Events: "[]", IngestedAt: now,
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", ParentSpanID: "p1", Name: "db.query", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 10, DurationUs: 500,
			Attributes: `{"db.system":"postgresql","db.statement":"SELECT * FROM orders WHERE id = 1","db.name":"shop"}`,
			Events:     "[]", IngestedAt: now,
		},
		{
			ProjectID: 1, TraceID: "t2", SpanID: "s2", ParentSpanID: "p1", Name: "db.query", Service: "web",
			Kind: "client", Status: "error", StartTimeUs: now.UnixMicro() + 20, DurationUs: 800,
			Attributes: `{"db.system":"postgresql","db.statement":"SELECT * FROM orders WHERE id = 2","exception.message":"boom"}`,
			Events:     "[]", IngestedAt: now,
		},
		{
			ProjectID: 1, TraceID: "t3", SpanID: "s3", Name: "db.query", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 30, DurationUs: 100,
			Attributes: `{"db.system":"postgresql","db.statement":"INSERT INTO logs (msg) VALUES ('x')"}`,
			Events:     "[]", IngestedAt: now,
		},
		{
			// Non-DB client span — must be ignored (no db.system).
			ProjectID: 1, TraceID: "t4", SpanID: "s4", Name: "http.get", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 40, DurationUs: 50,
			Attributes: `{"http.method":"GET"}`, Events: "[]", IngestedAt: now,
		},
	}
}

func TestListDatabaseQueries(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	now := time.Now()
	if err := repo.InsertSpans(dbTestSpans(now)); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	// Zero from/to skips the ingested_at filter (see GetServiceMap test).
	out, err := svc.ListDatabaseQueries(context.Background(), 1, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("ListDatabaseQueries: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 query patterns (SELECT + INSERT), got %d: %+v", len(out), out)
	}

	byOp := map[string]DatabaseQuerySummary{}
	for _, q := range out {
		byOp[q.Operation] = q
	}
	sel, ok := byOp["SELECT"]
	if !ok {
		t.Fatalf("expected a SELECT summary, got %+v", out)
	}
	if sel.CallCount != 2 {
		t.Errorf("SELECT CallCount = %d, want 2", sel.CallCount)
	}
	if sel.ErrorCount != 1 {
		t.Errorf("SELECT ErrorCount = %d, want 1", sel.ErrorCount)
	}
	if sel.DBSystem != "postgresql" {
		t.Errorf("SELECT DBSystem = %q, want postgresql", sel.DBSystem)
	}
	if _, ok := byOp["INSERT"]; !ok {
		t.Errorf("expected an INSERT summary, got %+v", out)
	}
}

func TestGetDatabaseQuerySpans(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	now := time.Now()
	if err := repo.InsertSpans(dbTestSpans(now)); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	pattern := NormalizeSQL("SELECT * FROM orders WHERE id = 1")
	spans, err := svc.GetDatabaseQuerySpans(context.Background(), 1, time.Time{}, time.Time{}, pattern, "")
	if err != nil {
		t.Fatalf("GetDatabaseQuerySpans: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("want 2 spans for the SELECT pattern, got %d: %+v", len(spans), spans)
	}

	// Caller enrichment comes from the parent server span p1.
	for _, s := range spans {
		if s.CallerName != "GET /orders" || s.CallerService != "web" {
			t.Errorf("span %s: caller = %q/%q, want 'GET /orders'/'web'", s.SpanID, s.CallerName, s.CallerService)
		}
	}
	// The errored execution must carry its message through.
	var sawErr bool
	for _, s := range spans {
		if s.Status == "error" {
			sawErr = true
			if s.ErrorMessage != "boom" {
				t.Errorf("error span message = %q, want boom", s.ErrorMessage)
			}
		}
	}
	if !sawErr {
		t.Error("expected one errored span in results")
	}
}

func TestParseAttrs(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		if got := parseAttrs(""); got != nil {
			t.Fatalf("expected nil for empty input, got %v", got)
		}
	})
	t.Run("empty object literal", func(t *testing.T) {
		if got := parseAttrs("{}"); got != nil {
			t.Fatalf("expected nil for {}, got %v", got)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		if got := parseAttrs("not-json"); got != nil {
			t.Fatalf("expected nil for malformed JSON, got %v", got)
		}
	})
	t.Run("valid json", func(t *testing.T) {
		got := parseAttrs(`{"db.system":"sqlite","db.name":"main"}`)
		if got == nil {
			t.Fatal("expected map, got nil")
		}
		if got["db.system"] != "sqlite" || got["db.name"] != "main" {
			t.Fatalf("unexpected map contents: %v", got)
		}
	})
}

func TestExtractSQLOperation(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM users":        "SELECT",
		"  INSERT INTO foo (x) ":     "INSERT",
		"DELETE":                     "DELETE",
		"UPDATE foo SET x=1":         "UPDATE",
		"":                           "",
		"   ":                        "",
		"WITH cte AS (...) SELECT *": "WITH",
	}
	for input, want := range cases {
		if got := extractSQLOperation(input); got != want {
			t.Errorf("extractSQLOperation(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQueryServiceCacheAccessors(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	if svc.Cache() != nil {
		t.Fatalf("Cache() should start nil, got %v", svc.Cache())
	}

	c := cache.New(cache.NewMemoryStore(), time.Minute)
	svc.SetCache(c)
	if svc.Cache() != c {
		t.Fatalf("SetCache did not propagate to Cache()")
	}
}
