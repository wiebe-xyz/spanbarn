package service

import (
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestDependencyAggregator(t *testing.T) {
	spans := []repository.Span{
		{SpanID: "a", Service: "web", Kind: "server", DurationUs: 10},
		{SpanID: "b", ParentSpanID: "a", Service: "web", Kind: "CLIENT", Attributes: `{"db.system":"sqlite"}`, DurationUs: 20, Status: "error"},
		{SpanID: "c", ParentSpanID: "a", Service: "api", Kind: "server", DurationUs: 30},
		{SpanID: "d", ParentSpanID: "a", Service: "web", Kind: "internal"},
		{SpanID: "e", ParentSpanID: "missing", Service: "api"},
	}
	agg := newDependencyAggregator()
	agg.addCalls(spans)
	got := agg.summaries(1)

	byTarget := map[string]DependencySummary{}
	for _, d := range got {
		byTarget[d.TargetType+":"+d.Target] = d
	}
	if len(got) != 2 {
		t.Fatalf("summaries = %+v", got)
	}
	db := byTarget["database:sqlite"]
	if db.CallCount != 1 || db.ErrorCount != 1 || db.ErrorRate != 1 {
		t.Errorf("database dependency = %+v", db)
	}
	svc := byTarget["service:api"]
	if svc.CallCount != 1 || svc.ErrorCount != 0 {
		t.Errorf("service dependency = %+v", svc)
	}
}

func TestDependencyTraceIDs(t *testing.T) {
	db := `{"db.system":"sqlite"}`
	spans := []repository.Span{
		{TraceID: "t1", Kind: "client", Attributes: db},
		{TraceID: "t1", Kind: "client", Attributes: db},
		{TraceID: "t2", Kind: "server", Attributes: db},
		{TraceID: "t3", Kind: "CLIENT", Attributes: db},
		{TraceID: "t4", Kind: "client", Attributes: `{"db.system":"pg"}`},
		{TraceID: "t5", Kind: "client", Attributes: db},
	}
	got := dependencyTraceIDs(spans, "sqlite", "database", 2)
	if len(got) != 2 || got[0] != "t1" || got[1] != "t3" {
		t.Errorf("limited ids = %v", got)
	}
	got = dependencyTraceIDs(spans, "sqlite", "database", 10)
	if len(got) != 3 || got[2] != "t5" {
		t.Errorf("all ids = %v", got)
	}
}

func TestSummarizeTraceSpans(t *testing.T) {
	spans := []repository.Span{
		{SpanID: "c", ParentSpanID: "r", Name: "child", DurationUs: 90},
		{SpanID: "r", Name: "root", Service: "web", Status: "error", DurationUs: 50, StartTimeUs: 1_000_000},
	}
	got := summarizeTraceSpans("t", spans)
	if got.RootSpanName != "root" || got.RootService != "web" || got.Status != "error" {
		t.Errorf("root fields = %+v", got)
	}
	if got.DurationUs != 90 || got.SpanCount != 2 || got.TraceID != "t" {
		t.Errorf("rollup fields = %+v", got)
	}
}
