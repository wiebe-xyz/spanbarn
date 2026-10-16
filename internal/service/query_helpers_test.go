package service

import (
	"fmt"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestFilterAndDedupeSpans(t *testing.T) {
	spans := []repository.Span{
		{SpanID: "a", ProjectID: 1},
		{SpanID: "b", ProjectID: 2},
		{SpanID: "a", ProjectID: 1},
	}
	if got := filterSpansByProject(spans, 0); len(got) != 3 {
		t.Errorf("zero project keeps all, got %d", len(got))
	}
	if got := filterSpansByProject(spans, 2); len(got) != 1 || got[0].SpanID != "b" {
		t.Errorf("project filter = %+v", got)
	}
	if got := dedupeSpansByID(spans); len(got) != 2 || got[0].SpanID != "a" || got[1].SpanID != "b" {
		t.Errorf("dedupe = %+v", got)
	}
	if got := dedupeSpansByID(nil); got != nil {
		t.Errorf("dedupe nil = %+v", got)
	}
}

func TestTraceRootSpan(t *testing.T) {
	spans := []repository.Span{{SpanID: "c", ParentSpanID: "r"}, {SpanID: "r"}}
	if got := traceRootSpan(spans); got.SpanID != "r" {
		t.Errorf("root = %s", got.SpanID)
	}
	orphans := []repository.Span{{SpanID: "x", ParentSpanID: "p"}, {SpanID: "y", ParentSpanID: "p"}}
	if got := traceRootSpan(orphans); got.SpanID != "x" {
		t.Errorf("fallback root = %s", got.SpanID)
	}
}

func TestTruncateTraceSpans(t *testing.T) {
	small := []repository.Span{{SpanID: "a"}}
	got, truncated := truncateTraceSpans(small, &small[0])
	if truncated || len(got) != 1 {
		t.Fatalf("small trace truncated: %v %d", truncated, len(got))
	}

	big := make([]repository.Span, MaxTraceDetailSpans+10)
	for i := range big {
		big[i] = repository.Span{SpanID: fmt.Sprintf("s%d", i), ParentSpanID: "p"}
	}
	big[len(big)-1] = repository.Span{SpanID: "root"}
	root := traceRootSpan(big)
	got, truncated = truncateTraceSpans(big, root)
	if !truncated || len(got) != MaxTraceDetailSpans {
		t.Fatalf("truncated=%v len=%d", truncated, len(got))
	}
	if got[0].SpanID != "root" {
		t.Errorf("root not kept in truncated view, first = %s", got[0].SpanID)
	}

	rootFirst := make([]repository.Span, MaxTraceDetailSpans+1)
	for i := range rootFirst {
		rootFirst[i] = repository.Span{SpanID: fmt.Sprintf("t%d", i), ParentSpanID: "p"}
	}
	rootFirst[0].ParentSpanID = ""
	got, _ = truncateTraceSpans(rootFirst, traceRootSpan(rootFirst))
	if got[0].SpanID != "t0" || got[1].SpanID != "t1" {
		t.Errorf("root at front must not displace spans: %s %s", got[0].SpanID, got[1].SpanID)
	}
}

func TestMatchDatabaseQuerySpan(t *testing.T) {
	stmt := `{"db.system":"sqlite","db.statement":"SELECT 1","exception.message":"boom"}`
	pattern := NormalizeSQL("SELECT 1")

	msg, ok := matchDatabaseQuerySpan(repository.Span{Kind: "client", Attributes: stmt}, pattern)
	if !ok || msg != "boom" {
		t.Errorf("statement match = %q, %v", msg, ok)
	}
	if _, ok := matchDatabaseQuerySpan(repository.Span{Kind: "server", Attributes: stmt}, pattern); ok {
		t.Error("server span must not match")
	}
	if _, ok := matchDatabaseQuerySpan(repository.Span{Kind: "client", Attributes: stmt}, "other"); ok {
		t.Error("other pattern must not match")
	}
	if _, ok := matchDatabaseQuerySpan(repository.Span{Kind: "client", Attributes: `{"x":"y"}`}, pattern); ok {
		t.Error("span without db.system must not match")
	}
	named := repository.Span{Kind: "CLIENT", Name: "Query Users", Attributes: `{"db.system":"pg","db.error.message":"slow"}`}
	msg, ok = matchDatabaseQuerySpan(named, "query users")
	if !ok || msg != "slow" {
		t.Errorf("name match = %q, %v", msg, ok)
	}
}

func TestFirstStringAttr(t *testing.T) {
	attrs := map[string]any{"b": "second", "c": "third"}
	if got := firstStringAttr(attrs, "a", "b", "c"); got != "second" {
		t.Errorf("got %q", got)
	}
	if got := firstStringAttr(attrs, "x"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestServiceSummariesWeightsAndOrder(t *testing.T) {
	merged := map[string]*aggStats{}
	statsFor(merged, "small").foldAggregate(1, repository.Aggregate{Count: 1, P50Us: 10, P95Us: 20, P99Us: 30})
	big := statsFor(merged, "big")
	big.foldAggregate(1, repository.Aggregate{Count: 3, ErrorCount: 1, P50Us: 100, P95Us: 200, P99Us: 300})
	big.foldAggregate(1, repository.Aggregate{Count: 1, P50Us: 200, P95Us: 400, P99Us: 600})
	if statsFor(merged, "big") != big {
		t.Fatal("statsFor must return the existing entry")
	}

	got := serviceSummaries(merged)
	if len(got) != 2 || got[0].Service != "big" {
		t.Fatalf("order = %+v", got)
	}
	if got[0].SpanCount != 4 || got[0].ErrorCount != 1 || got[0].P50Us != 125 {
		t.Errorf("big = %+v", got[0])
	}
}

// TestEstimateCount checks that a fully weighted row counts its weights and
// any other row falls back to scaling its ok spans by the current rate.
func TestEstimateCount(t *testing.T) {
	cases := []struct {
		name string
		a    repository.Aggregate
		want int64
	}{
		// One error trace span (weight 1) and one span of a trace sampled 1 in
		// 1000: the ratio estimate would scale both ok spans.
		{"weighted", repository.Aggregate{Count: 3, ErrorCount: 1, WeightedCount: 1002, WeightedSpans: 3}, 1002},
		{"no weights", repository.Aggregate{Count: 3, ErrorCount: 1}, 2001},
		{"partly weighted", repository.Aggregate{Count: 3, ErrorCount: 1, WeightedCount: 2, WeightedSpans: 2}, 2001},
	}
	for _, c := range cases {
		if got := estimateCount(c.a, 0.001); got != c.want {
			t.Errorf("%s: estimateCount = %d, want %d", c.name, got, c.want)
		}
	}
}
