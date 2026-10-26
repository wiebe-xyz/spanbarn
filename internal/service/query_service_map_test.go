package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestGetServiceMap(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	now := time.Now()
	// web (server) → api (cross-service child) → client span out of api.
	spans := []repository.Span{
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "GET /", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 1000,
			Attributes: "{}", Events: "[]", IngestedAt: now,
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s2", ParentSpanID: "s1", Name: "handle", Service: "api",
			Kind: "server", Status: "error", StartTimeUs: now.UnixMicro() + 100, DurationUs: 500,
			Attributes: "{}", Events: "[]", IngestedAt: now,
		},
		{
			ProjectID: 1, TraceID: "t1", SpanID: "s3", ParentSpanID: "s2", Name: "SELECT", Service: "api",
			Kind: "client", Status: "ok", StartTimeUs: now.UnixMicro() + 200, DurationUs: 300,
			Attributes: "{}", Events: "[]", IngestedAt: now,
		},
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	// Zero from/to skips the ingested_at filter so the map is built from all
	// spans in the project, independent of insert-time storage format.
	m, err := svc.GetServiceMap(context.Background(), 1, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetServiceMap: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil service map")
	}

	nodeByID := map[string]ServiceMapNode{}
	for _, n := range m.Nodes {
		nodeByID[n.ID] = n
	}
	if _, ok := nodeByID["web"]; !ok {
		t.Errorf("expected a 'web' node, got %+v", m.Nodes)
	}
	if _, ok := nodeByID["api"]; !ok {
		t.Errorf("expected an 'api' node, got %+v", m.Nodes)
	}

	// The cross-service parent/child link (web → api) must produce a service edge.
	var found bool
	for _, e := range m.Edges {
		if e.Source == "web" && e.Target == "api" {
			found = true
			if e.CallCount != 1 {
				t.Errorf("web→api CallCount = %d, want 1", e.CallCount)
			}
		}
	}
	if !found {
		t.Errorf("expected a web→api edge, got %+v", m.Edges)
	}
}

// A range with more spans than the map reads must say which part it covers.
func TestGetServiceMapReportsTruncatedCoverage(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	limit := serviceMapSpanLimit
	serviceMapSpanLimit = 2
	t.Cleanup(func() { serviceMapSpanLimit = limit })

	now := time.Now()
	var spans []repository.Span
	for _, id := range []string{"s1", "s2", "s3"} {
		spans = append(spans, repository.Span{
			ProjectID: 1, TraceID: "t" + id, SpanID: id, Name: "GET /", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: now.UnixMicro(), DurationUs: 1000,
			Attributes: "{}", Events: "[]", IngestedAt: now,
		})
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	m, err := svc.GetServiceMap(context.Background(), 1, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetServiceMap: %v", err)
	}
	if !m.Truncated || m.CoveredFrom == nil {
		t.Fatalf("Truncated = %v, CoveredFrom = %v; want a truncated map with its start", m.Truncated, m.CoveredFrom)
	}

	serviceMapSpanLimit = 10
	m, err = svc.GetServiceMap(context.Background(), 1, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetServiceMap: %v", err)
	}
	if m.Truncated || m.CoveredFrom != nil {
		t.Errorf("a map that read every span: Truncated = %v, CoveredFrom = %v; want neither", m.Truncated, m.CoveredFrom)
	}
}

func TestMarkCoverageTakesTheOldestSpan(t *testing.T) {
	limit := serviceMapSpanLimit
	serviceMapSpanLimit = 3
	t.Cleanup(func() { serviceMapSpanLimit = limit })
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	spans := []repository.Span{{IngestedAt: base.Add(2 * time.Minute)}, {IngestedAt: base}, {IngestedAt: base.Add(time.Minute)}}

	m := &ServiceMap{}
	markCoverage(m, spans)
	if !m.Truncated || m.CoveredFrom == nil || !m.CoveredFrom.Equal(base) {
		t.Errorf("Truncated = %v, CoveredFrom = %v; want true, %v", m.Truncated, m.CoveredFrom, base)
	}

	m = &ServiceMap{}
	markCoverage(m, spans[:2])
	if m.Truncated || m.CoveredFrom != nil {
		t.Errorf("under the limit: Truncated = %v, CoveredFrom = %v; want neither", m.Truncated, m.CoveredFrom)
	}
}
