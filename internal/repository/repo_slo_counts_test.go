package repository

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func TestCountSpansSplitsErrorsAndHonoursWindowAndExpr(t *testing.T) {
	repo := setupTestDB(t)
	mk := func(id, kind, status string) Span {
		s := filterSpan("t"+id, id, "op", "web", kind, `{}`, 10)
		s.Status = status
		return s
	}
	if err := repo.InsertSpans([]Span{
		mk("a", "server", "ok"), mk("b", "server", "error"), mk("c", "server", "ERROR"), mk("d", "client", "ok"),
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().Add(-time.Hour)

	c, err := repo.CountSpans(SpanFilter{ProjectID: 1, From: from})
	if err != nil || c.Total != 4 || c.Errors != 2 {
		t.Fatalf("all = %d/%d err %v", c.Total, c.Errors, err)
	}

	expr, _ := filter.Parse(`{"match":"and","filters":[{"key":"kind","op":"=","value":"server"}]}`)
	c, err = repo.CountSpans(SpanFilter{ProjectID: 1, Expr: expr, From: from})
	if err != nil || c.Total != 3 || c.Errors != 2 {
		t.Fatalf("server = %d/%d err %v", c.Total, c.Errors, err)
	}

	c, err = repo.CountSpans(SpanFilter{ProjectID: 1, From: time.Now().UTC().Add(time.Hour)})
	if err != nil || c.Total != 0 {
		t.Fatalf("future window = %d err %v", c.Total, err)
	}

	if _, err := repo.CountSpans(SpanFilter{ProjectID: 1, Expr: expr}); err == nil {
		t.Fatal("expression without From must be refused")
	}
}

// TestCountSpansSumsSampleWeights checks that CountSpans reports the weights
// the spans carry, and how many carried one, so an SLO can count a trace kept
// for an error once and a ratio-sampled trace for its ratio (#333).
func TestCountSpansSumsSampleWeights(t *testing.T) {
	repo := setupTestDB(t)
	mk := func(id, status string, weight int) Span {
		s := filterSpan("t"+id, id, "op", "web", "server", `{}`, 10)
		s.Status = status
		s.SampleWeight = weight
		return s
	}
	if err := repo.InsertSpans([]Span{mk("a", "ok", 1000), mk("b", "error", 1), mk("c", "ok", 1)}); err != nil {
		t.Fatal(err)
	}
	from := time.Now().UTC().Add(-time.Hour)
	c, err := repo.CountSpans(SpanFilter{ProjectID: 1, From: from})
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := c.Weights.Estimate(c.Total); !ok || n != 1002 {
		t.Fatalf("estimate = %d, %v; want 1002 from weights 1000+1+1", n, ok)
	}

	if err := repo.InsertSpans([]Span{mk("d", "ok", 0)}); err != nil {
		t.Fatal(err)
	}
	c, err = repo.CountSpans(SpanFilter{ProjectID: 1, From: from})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Weights.Estimate(c.Total); ok || c.Weights.Spans != 3 || c.Total != 4 {
		t.Fatalf("with an unweighted span: weights %+v of %d spans, estimate ok %v; want no estimate", c.Weights, c.Total, ok)
	}
}
