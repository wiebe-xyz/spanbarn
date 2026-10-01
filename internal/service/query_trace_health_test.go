package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func healthSpan(trace, span, parent, name string) repository.Span {
	return repository.Span{
		ProjectID: 1, TraceID: trace, SpanID: span, ParentSpanID: parent, Name: name,
		Service: "web", Kind: "server", Status: "ok", DurationUs: 10, Attributes: "{}", Events: "[]",
	}
}

func healthQuery() TraceHealthQuery {
	now := time.Now().UTC()
	return TraceHealthQuery{ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
}

func TestTraceHealthValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	cases := map[string]TraceHealthQuery{
		"missing project": {From: now.Add(-time.Hour), To: now},
		"missing range":   {ProjectID: 1},
		"inverted":        {ProjectID: 1, From: now, To: now.Add(-time.Hour)},
		"too wide":        {ProjectID: 1, From: now.Add(-8 * 24 * time.Hour), To: now},
	}
	ctx := context.Background()
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ListOrphanSpans(ctx, q); !errors.Is(err, ErrInvalidTraceHealthRequest) {
				t.Errorf("orphans err = %v", err)
			}
			if _, err := svc.ListSingleSpanTraces(ctx, q); !errors.Is(err, ErrInvalidTraceHealthRequest) {
				t.Errorf("single err = %v", err)
			}
			if _, err := svc.ListSpanNames(ctx, q); !errors.Is(err, ErrInvalidTraceHealthRequest) {
				t.Errorf("names err = %v", err)
			}
			if _, err := svc.ListRootlessTraces(ctx, q); !errors.Is(err, ErrInvalidTraceHealthRequest) {
				t.Errorf("rootless err = %v", err)
			}
		})
	}
}

func TestTraceHealthViews(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	ctx := context.Background()
	if err := repo.InsertSpans([]repository.Span{
		healthSpan("one", "a", "", "job.run"),
		healthSpan("lost", "b", "ghost", "POST /presign"),
		healthSpan("tree", "c", "", "GET /x"),
		healthSpan("tree", "d", "c", "SELECT"),
	}); err != nil {
		t.Fatal(err)
	}
	q := healthQuery()

	orphans, err := svc.ListOrphanSpans(ctx, q)
	if err != nil || len(orphans) != 1 || orphans[0].Name != "POST /presign" || orphans[0].SampleTraceID != "lost" {
		t.Fatalf("orphans: %v %+v", err, orphans)
	}
	single, err := svc.ListSingleSpanTraces(ctx, q)
	if err != nil || len(single) != 2 {
		t.Fatalf("single: %v %+v", err, single)
	}
	names, err := svc.ListSpanNames(ctx, q)
	if err != nil || len(names) != 4 {
		t.Fatalf("names: %v %+v", err, names)
	}
	rootless, err := svc.ListRootlessTraces(ctx, q)
	if err != nil || rootless.Total != 1 || len(rootless.Traces) != 1 {
		t.Fatalf("rootless: %v %+v", err, rootless)
	}
	tr := rootless.Traces[0]
	if tr.TraceID != "lost" || tr.HasRoot == nil || *tr.HasRoot || tr.RootSpanName != "" || tr.OrphanCount != 1 {
		t.Errorf("rootless trace = %+v", tr)
	}

	// The limit is clamped, not an error.
	q.Limit = 100000
	if _, err := svc.ListSpanNames(ctx, q); err != nil {
		t.Errorf("oversized limit: %v", err)
	}
}
