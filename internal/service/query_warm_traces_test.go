package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// summaryRecorder records every trace list read the service issues.
type summaryRecorder struct {
	QueryRepository
	filters []repository.SpanFilter
	cancel  func()
}

func (r *summaryRecorder) SearchTraceSummaries(f repository.SpanFilter, _ int) ([]repository.TraceSummaryRow, error) {
	r.filters = append(r.filters, f)
	if r.cancel != nil {
		r.cancel()
	}
	return nil, nil
}

func (r *summaryRecorder) ExcludedOperations(int64) ([]string, error) { return nil, nil }

func TestWarmTraceListsReadsTheTracesPageDefaultPerProject(t *testing.T) {
	rec := &summaryRecorder{}
	svc := NewQueryService(rec, nil, nil)
	now := time.Date(2026, 11, 19, 6, 30, 0, 0, time.UTC)

	n, err := svc.WarmTraceLists(context.Background(), []int64{7, 9}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(rec.filters) != 3 {
		t.Fatalf("reads = %d (recorded %d), want all projects plus two", n, len(rec.filters))
	}
	for i, want := range []int64{0, 7, 9} {
		f := rec.filters[i]
		if f.ProjectID != want {
			t.Errorf("read %d project = %d, want %d", i, f.ProjectID, want)
		}
		if !f.RootOnly || !f.SortErrorsFirst || f.Limit != 50 {
			t.Errorf("read %d shape = root %v errors first %v limit %d, want the traces page default", i, f.RootOnly, f.SortErrorsFirst, f.Limit)
		}
		if !f.To.Equal(now) || !f.From.Equal(now.Add(-time.Hour)) {
			t.Errorf("read %d range = %s to %s, want the newest hour", i, f.From, f.To)
		}
	}
}

func TestWarmTraceListsStopsBetweenReadsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &summaryRecorder{cancel: cancel}
	svc := NewQueryService(rec, nil, nil)

	n, err := svc.WarmTraceLists(ctx, []int64{7, 9}, time.Now())
	if err == nil {
		t.Fatal("err = nil, want the cancellation")
	}
	if n != 1 || len(rec.filters) != 1 {
		t.Errorf("reads = %d (recorded %d), want 1: the second read must not start", n, len(rec.filters))
	}
}
