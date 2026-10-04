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

	total, errs, err := repo.CountSpans(SpanFilter{ProjectID: 1, From: from})
	if err != nil || total != 4 || errs != 2 {
		t.Fatalf("all = %d/%d err %v", total, errs, err)
	}

	expr, _ := filter.Parse(`{"match":"and","filters":[{"key":"kind","op":"=","value":"server"}]}`)
	total, errs, err = repo.CountSpans(SpanFilter{ProjectID: 1, Expr: expr, From: from})
	if err != nil || total != 3 || errs != 2 {
		t.Fatalf("server = %d/%d err %v", total, errs, err)
	}

	total, _, err = repo.CountSpans(SpanFilter{ProjectID: 1, From: time.Now().UTC().Add(time.Hour)})
	if err != nil || total != 0 {
		t.Fatalf("future window = %d err %v", total, err)
	}

	if _, _, err := repo.CountSpans(SpanFilter{ProjectID: 1, Expr: expr}); err == nil {
		t.Fatal("expression without From must be refused")
	}
}
