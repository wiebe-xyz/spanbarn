package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func versionSpan(id, status, version string, durationUs int64) repository.Span {
	return repository.Span{
		ProjectID: 1, TraceID: "t" + id, SpanID: "s" + id, Name: "GET /", Service: "web",
		Kind: "server", Status: status, StartTimeUs: time.Now().UnixMicro(), DurationUs: durationUs,
		Attributes: `{"service.version":"` + version + `"}`, Events: `[]`,
	}
}

func TestListServiceVersions(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	if err := repo.InsertSpans([]repository.Span{
		versionSpan("1", "ok", "1.0.0", 100),
		versionSpan("2", "error", "1.0.0", 300),
		versionSpan("3", "ok", "1.1.0", 50),
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	got, err := svc.ListServiceVersions(context.Background(), 1, "web", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListServiceVersions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 versions, got %d: %+v", len(got), got)
	}
	for _, v := range got {
		switch v.Version {
		case "1.0.0":
			if v.SpanCount != 2 || v.ErrorCount != 1 || v.ErrorRate != 0.5 || v.P95Us != 300 {
				t.Errorf("1.0.0 = %+v, want 2 spans, 1 error, rate 0.5, p95 300", v)
			}
		case "1.1.0":
			if v.SpanCount != 1 || v.ErrorRate != 0 {
				t.Errorf("1.1.0 = %+v, want 1 span, rate 0", v)
			}
		default:
			t.Errorf("unexpected version %q", v.Version)
		}
	}
}

func TestListServiceVersionsWithoutVersions(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	s := versionSpan("1", "ok", "x", 10)
	s.Attributes = `{}`
	if err := repo.InsertSpans([]repository.Span{s}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListServiceVersions(context.Background(), 1, "web", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ListServiceVersions: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no versions, got %+v", got)
	}
}
