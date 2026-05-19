package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"

	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

func setupTestRepo(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return repository.NewRepository(db.DB)
}

func TestListServices(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

	// Insert aggregates for 2 services.
	if err := repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /", Bucket: bucket,
		Count: 100, ErrorCount: 5, P50Us: 1000, P95Us: 5000, P99Us: 10000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "worker", Operation: "process", Bucket: bucket,
		Count: 50, ErrorCount: 2, P50Us: 2000, P95Us: 8000, P99Us: 15000,
	}); err != nil {
		t.Fatal(err)
	}

	from := bucket.Add(-time.Hour)
	to := bucket.Add(time.Hour)

	result, err := svc.ListServices(context.Background(), 1, from, to, false)
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 services, got %d", len(result))
	}

	// Results sorted by spanCount desc: web (100) first, worker (50) second.
	if result[0].Service != "web" {
		t.Errorf("expected first service 'web', got %q", result[0].Service)
	}
	if result[0].SpanCount != 100 {
		t.Errorf("expected spanCount 100, got %d", result[0].SpanCount)
	}
	if result[0].ErrorCount != 5 {
		t.Errorf("expected errorCount 5, got %d", result[0].ErrorCount)
	}
	if result[0].ErrorRate < 0.04 || result[0].ErrorRate > 0.06 {
		t.Errorf("expected errorRate ~0.05, got %f", result[0].ErrorRate)
	}

	if result[1].Service != "worker" {
		t.Errorf("expected second service 'worker', got %q", result[1].Service)
	}
	if result[1].SpanCount != 50 {
		t.Errorf("expected spanCount 50, got %d", result[1].SpanCount)
	}
}

func TestListOperations(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

	_ = repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /api", Resource: "/api", Kind: "server",
		Bucket: bucket, Count: 80, ErrorCount: 3, P50Us: 500, P95Us: 2000, P99Us: 5000,
	})
	_ = repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "POST /data", Resource: "/data", Kind: "server",
		Bucket: bucket, Count: 20, ErrorCount: 1, P50Us: 1000, P95Us: 4000, P99Us: 8000,
	})
	_ = repo.UpsertAggregate(repository.Aggregate{
		ProjectID: 1, Service: "worker", Operation: "process", Resource: "", Kind: "internal",
		Bucket: bucket, Count: 50, ErrorCount: 0, P50Us: 3000, P95Us: 6000, P99Us: 9000,
	})

	from := bucket.Add(-time.Hour)
	to := bucket.Add(time.Hour)

	result, err := svc.ListOperations(context.Background(), 1, "web", from, to, "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 operations for 'web', got %d", len(result))
	}

	// Sorted by spanCount desc.
	if result[0].Operation != "GET /api" {
		t.Errorf("expected first operation 'GET /api', got %q", result[0].Operation)
	}
	if result[0].SpanCount != 80 {
		t.Errorf("expected spanCount 80, got %d", result[0].SpanCount)
	}
	if result[1].Operation != "POST /data" {
		t.Errorf("expected second operation 'POST /data', got %q", result[1].Operation)
	}
}

func TestGetTimeseries(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

	// Insert aggregates across 3 minute-level buckets.
	for i := 0; i < 3; i++ {
		bucket := base.Add(time.Duration(i) * time.Minute)
		_ = repo.UpsertAggregate(repository.Aggregate{
			ProjectID: 1, Service: "web", Operation: "GET /",
			Bucket: bucket, Count: int64(10 + i), ErrorCount: int64(i),
			P50Us: 500, P95Us: 2000, P99Us: 5000,
		})
	}

	from := base.Add(-time.Minute)
	to := base.Add(5 * time.Minute)

	result, err := svc.GetTimeseries(context.Background(), 1, "web", "GET /", from, to, time.Minute)
	if err != nil {
		t.Fatalf("GetTimeseries: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(result))
	}

	// Verify ordering is ascending.
	for i := 1; i < len(result); i++ {
		if !result[i].Bucket.After(result[i-1].Bucket) {
			t.Errorf("buckets not in ascending order at index %d", i)
		}
	}

	// Verify first bucket values.
	if result[0].Count != 10 {
		t.Errorf("expected count 10 for first bucket, got %d", result[0].Count)
	}
}

func TestSearchTraces(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	// Insert spans for 2 traces.
	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "trace-1", SpanID: "s1", Name: "GET /api", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 1000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-1", SpanID: "s2", ParentSpanID: "s1", Name: "DB query", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 100, DurationUs: 500,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-2", SpanID: "s3", Name: "POST /data", Service: "web",
			Kind: "server", Status: "error", StartTimeUs: now.UnixMicro() + 2000, DurationUs: 3000,
			Attributes: "{}", Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	// Search all traces.
	result, err := svc.SearchTraces(context.Background(), TraceSearchFilter{
		Service: "web",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(result))
	}

	// Search with status filter.
	errorTraces, err := svc.SearchTraces(context.Background(), TraceSearchFilter{
		Service: "web",
		Status:  "error",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("SearchTraces error: %v", err)
	}
	if len(errorTraces) != 1 {
		t.Fatalf("expected 1 error trace, got %d", len(errorTraces))
	}
	if errorTraces[0].TraceID != "trace-2" {
		t.Errorf("expected trace-2, got %q", errorTraces[0].TraceID)
	}

	// Search with min duration filter.
	slowTraces, err := svc.SearchTraces(context.Background(), TraceSearchFilter{
		MinDurationUs: 2000,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("SearchTraces slow: %v", err)
	}
	if len(slowTraces) != 1 {
		t.Fatalf("expected 1 slow trace, got %d", len(slowTraces))
	}
}

func TestGetTrace(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "trace-abc", SpanID: "root", Name: "GET /", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 5000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-abc", SpanID: "child1", ParentSpanID: "root", Name: "DB query", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 100, DurationUs: 2000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "trace-abc", SpanID: "child2", ParentSpanID: "root", Name: "Cache", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 200, DurationUs: 100,
			Attributes: "{}", Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	detail, err := svc.GetTrace(context.Background(),"trace-abc")
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	if detail == nil {
		t.Fatal("expected trace detail, got nil")
	}
	if detail.TraceID != "trace-abc" {
		t.Errorf("expected traceId 'trace-abc', got %q", detail.TraceID)
	}
	if len(detail.Spans) != 3 {
		t.Errorf("expected 3 spans, got %d", len(detail.Spans))
	}
	if detail.Service != "web" {
		t.Errorf("expected service 'web', got %q", detail.Service)
	}
	if detail.Name != "GET /" {
		t.Errorf("expected name 'GET /', got %q", detail.Name)
	}
	if detail.DurationUs != 5000 {
		t.Errorf("expected durationUs 5000, got %d", detail.DurationUs)
	}

	// Non-existent trace.
	notFound, err := svc.GetTrace(context.Background(),"non-existent")
	if err != nil {
		t.Fatalf("GetTrace non-existent: %v", err)
	}
	if notFound != nil {
		t.Errorf("expected nil for non-existent trace, got %+v", notFound)
	}
}

func TestGetWebVitals(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "wv1", Name: "webvital.LCP", Service: "frontend",
			Kind: "internal", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 2500_000,
			Attributes: `{"webvital.page":"/home","webvital.rating":"good"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "wv2", Name: "webvital.LCP", Service: "frontend",
			Kind: "internal", Status: "ok", StartTimeUs: now.UnixMicro() + 1000, DurationUs: 4500_000,
			Attributes: `{"webvital.page":"/home","webvital.rating":"poor"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "wv3", Name: "webvital.CLS", Service: "frontend",
			Kind: "internal", Status: "ok", StartTimeUs: now.UnixMicro() + 2000, DurationUs: 100_000,
			Attributes: `{"webvital.page":"/home","webvital.rating":"good"}`, Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	result, err := svc.GetWebVitals(context.Background(), "", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWebVitals: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 web vital summaries, got %d", len(result))
	}

	var lcp *WebVitalSummary
	for i := range result {
		if result[i].Metric == "LCP" {
			lcp = &result[i]
		}
	}
	if lcp == nil {
		t.Fatal("expected LCP metric")
	}
	if lcp.Samples != 2 {
		t.Errorf("expected 2 LCP samples, got %d", lcp.Samples)
	}
	if lcp.Good != 1 || lcp.Poor != 1 {
		t.Errorf("expected 1 good + 1 poor, got good=%d poor=%d", lcp.Good, lcp.Poor)
	}
}

func TestGetWebVitalsTimeseries(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "wv1", Name: "webvital.LCP", Service: "frontend",
			Kind: "internal", Status: "ok", StartTimeUs: base.UnixMicro(), DurationUs: 2000_000,
			Attributes: `{"webvital.page":"/home","webvital.rating":"good"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "wv2", Name: "webvital.LCP", Service: "frontend",
			Kind: "internal", Status: "ok", StartTimeUs: base.Add(5 * time.Minute).UnixMicro(), DurationUs: 3000_000,
			Attributes: `{"webvital.page":"/home","webvital.rating":"needs-improvement"}`, Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	result, err := svc.GetWebVitalsTimeseries(context.Background(), "", "/home", "LCP", time.Time{}, time.Time{}, 5*time.Minute)
	if err != nil {
		t.Fatalf("GetWebVitalsTimeseries: %v", err)
	}
	if len(result) < 1 {
		t.Fatal("expected at least 1 bucket")
	}

	if result[0].P50Ms <= 0 {
		t.Errorf("expected positive p50Ms, got %f", result[0].P50Ms)
	}
	if result[0].Samples <= 0 {
		t.Errorf("expected positive samples, got %d", result[0].Samples)
	}
}

func TestListServicesFromSpansPercentiles(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "GET /api", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 1000,
			Attributes: "{}", Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s2", Name: "GET /api", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro() + 1000, DurationUs: 5000,
			Attributes: "{}", Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	result, err := svc.ListServices(context.Background(), 1, time.Time{}, time.Time{}, false)
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 service, got %d", len(result))
	}
	if result[0].P50Us == 0 {
		t.Error("expected non-zero P50Us from span percentiles")
	}
}

func TestListDependencies(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "DB call", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 500,
			Attributes: `{"db.system":"postgresql"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s2", Name: "DB call 2", Service: "web",
			Kind: "client", Status: "error", StartTimeUs: now.UnixMicro() + 100, DurationUs: 1000,
			Attributes: `{"db.system":"postgresql"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s3", Name: "HTTP call", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 200, DurationUs: 2000,
			Attributes: `{"http.url":"https://api.example.com/v1/data"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s4", Name: "Internal op", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro() + 300, DurationUs: 100,
			Attributes: `{}`, Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	deps, err := svc.ListDependencies(context.Background(), 1, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("ListDependencies: %v", err)
	}

	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(deps))
	}

	// Find postgresql dependency.
	var pgDep, httpDep *DependencySummary
	for i := range deps {
		switch deps[i].Target {
		case "postgresql":
			pgDep = &deps[i]
		case "api.example.com":
			httpDep = &deps[i]
		}
	}

	if pgDep == nil {
		t.Fatal("expected postgresql dependency")
	}
	if pgDep.TargetType != "database" {
		t.Errorf("expected targetType 'database', got %q", pgDep.TargetType)
	}
	if pgDep.CallCount != 2 {
		t.Errorf("expected callCount 2, got %d", pgDep.CallCount)
	}
	if pgDep.ErrorCount != 1 {
		t.Errorf("expected errorCount 1, got %d", pgDep.ErrorCount)
	}
	if pgDep.ErrorRate < 0.49 || pgDep.ErrorRate > 0.51 {
		t.Errorf("expected errorRate ~0.5, got %f", pgDep.ErrorRate)
	}

	if httpDep == nil {
		t.Fatal("expected api.example.com dependency")
	}
	if httpDep.TargetType != "http" {
		t.Errorf("expected targetType 'http', got %q", httpDep.TargetType)
	}
	if httpDep.CallCount != 1 {
		t.Errorf("expected callCount 1, got %d", httpDep.CallCount)
	}
}

func TestGetDependencyTraces(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil)

	now := time.Now()

	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "dt1", SpanID: "ds1", Name: "root", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 5000,
			Attributes: `{}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "dt1", SpanID: "ds2", ParentSpanID: "ds1", Name: "DB call", Service: "web",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 100, DurationUs: 800,
			Attributes: `{"db.system":"postgresql"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "dt2", SpanID: "ds3", Name: "root2", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro() + 10000, DurationUs: 3000,
			Attributes: `{}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "dt2", SpanID: "ds4", ParentSpanID: "ds3", Name: "DB call", Service: "web",
			Kind: "client", Status: "error", StartTimeUs: now.UnixMicro() + 10100, DurationUs: 1200,
			Attributes: `{"db.system":"postgresql"}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "dt3", SpanID: "ds5", Name: "HTTP root", Service: "api",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro() + 20000, DurationUs: 2000,
			Attributes: `{}`, Events: "[]",
		},
		{
			ProjectID: 1, TraceID: "dt3", SpanID: "ds6", ParentSpanID: "ds5", Name: "HTTP call", Service: "api",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 20100, DurationUs: 600,
			Attributes: `{"http.url":"https://example.com/data"}`, Events: "[]",
		},
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	traces, err := svc.GetDependencyTraces(context.Background(), 1, "postgresql", "database", time.Time{}, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("GetDependencyTraces: %v", err)
	}
	if len(traces) != 2 {
		t.Fatalf("expected 2 traces for postgresql, got %d", len(traces))
	}

	traces, err = svc.GetDependencyTraces(context.Background(), 1, "example.com", "http", time.Time{}, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("GetDependencyTraces: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace for example.com, got %d", len(traces))
	}
	if traces[0].TraceID != "dt3" {
		t.Errorf("expected traceID dt3, got %s", traces[0].TraceID)
	}

	traces, err = svc.GetDependencyTraces(context.Background(), 1, "nonexistent", "database", time.Time{}, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("GetDependencyTraces: %v", err)
	}
	if len(traces) != 0 {
		t.Fatalf("expected 0 traces for nonexistent, got %d", len(traces))
	}
}
