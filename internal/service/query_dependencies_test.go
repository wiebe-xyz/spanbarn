package service

import (
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestExtractDependencyTargetRules(t *testing.T) {
	tests := []struct {
		name       string
		attrs      string
		wantTarget string
		wantType   string
	}{
		{"empty", "", "", ""},
		{"empty object", "{}", "", ""},
		{"invalid json", "{", "", ""},
		{"db system", `{"db.system":"sqlite"}`, "sqlite", "database"},
		{"db system beats name", `{"db.system":"sqlite","db.name":"app"}`, "sqlite", "database"},
		{"db name", `{"db.name":"app"}`, "app", "database"},
		{"peer service", `{"peer.service":"billing"}`, "billing", "service"},
		{"rpc", `{"rpc.service":"Greeter"}`, "Greeter", "rpc"},
		{"messaging", `{"messaging.system":"kafka"}`, "kafka", "messaging"},
		{"aws", `{"aws.service":"S3"}`, "S3", "aws"},
		{"http url host", `{"http.url":"https://api.example.com/v1/x"}`, "api.example.com", "http"},
		{"url full host", `{"url.full":"https://other.example.com/y"}`, "other.example.com", "http"},
		{"http host", `{"http.host":"h.example.com"}`, "h.example.com", "http"},
		{"server address", `{"server.address":"10.0.0.1"}`, "10.0.0.1", "network"},
		{"net peer name", `{"net.peer.name":"peer"}`, "peer", "network"},
		{"nothing matches", `{"other":"x"}`, "", ""},
	}
	for _, tc := range tests {
		target, typ := extractDependencyTarget(tc.attrs)
		if target != tc.wantTarget || typ != tc.wantType {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, target, typ, tc.wantTarget, tc.wantType)
		}
	}
}

func TestDependencyAggregator(t *testing.T) {
	spans := []repository.Span{
		{SpanID: "a", Service: "web", Kind: "server", DurationUs: 10},
		{SpanID: "b", ParentSpanID: "a", Service: "web", Kind: "CLIENT", Attributes: `{"db.system":"sqlite"}`, DurationUs: 20, Status: "error"},
		{SpanID: "c", ParentSpanID: "a", Service: "api", Kind: "server", DurationUs: 30},
		{SpanID: "d", ParentSpanID: "a", Service: "web", Kind: "internal"},
		{SpanID: "e", ParentSpanID: "missing", Service: "api"},
	}
	agg := newDependencyAggregator()
	agg.addClientSpans(spans)
	agg.addCrossServiceCalls(spans)
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
