package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestSearchSpansValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	cases := map[string]SpanSearchFilter{
		"missing project": {From: now.Add(-time.Hour)},
		"missing from":    {ProjectID: 1},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.SearchSpans(context.Background(), f); !errors.Is(err, filter.ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSearchSpansFiltersAndShapesResults(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	mk := func(id, attrs string) repository.Span {
		return repository.Span{
			ProjectID: 1, TraceID: "t-" + id, SpanID: id, Name: "GET /x", Service: "web",
			Kind: "server", Status: "ok", StartTimeUs: 1_000_000, DurationUs: 42,
			Attributes: attrs, Events: "[]",
		}
	}
	if err := repo.InsertSpans([]repository.Span{
		mk("a", `{"user":"ann"}`),
		mk("b", `{"user":"bob"}`),
		mk("c", `broken`),
	}); err != nil {
		t.Fatal(err)
	}
	expr, err := filter.Parse(`{"filters":[{"key":"user","op":"=","value":"ann"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	got, err := svc.SearchSpans(context.Background(), SpanSearchFilter{
		ProjectID: 1, Expr: expr, From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 1000,
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
	if got[0].SpanID != "a" || string(got[0].Attributes) != `{"user":"ann"}` || got[0].DurationUs != 42 {
		t.Fatalf("unexpected span %+v", got[0])
	}

	// Unfiltered, a span with broken attributes still lists, with null attributes.
	all, err := svc.SearchSpans(context.Background(), SpanSearchFilter{ProjectID: 1, From: now.Add(-time.Hour)})
	if err != nil || len(all) != 3 {
		t.Fatalf("all: %v, %v", all, err)
	}
	for _, s := range all {
		if s.SpanID == "c" && string(s.Attributes) != "null" {
			t.Errorf("broken attributes = %s, want null", s.Attributes)
		}
	}
}
