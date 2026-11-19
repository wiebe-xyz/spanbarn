package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type fakeProjectLister struct {
	service.ProjectRepository
	list []repository.Project
	err  error
}

func (f fakeProjectLister) ListProjects() ([]repository.Project, error) { return f.list, f.err }

func TestWarmTraceListsOnceSurvivesAProjectListFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A nil query service would panic if the read ran, so a returned call shows
	// the failed listing ended the pass before any read.
	warmTraceListsOnce(context.Background(), nil, fakeProjectLister{err: errors.New("db busy")}, logger)
}

// recordingSearch counts the trace list reads the warm loop issues.
type recordingSearch struct {
	service.QueryRepository
	reads chan repository.SpanFilter
}

func (r recordingSearch) SearchTraceSummaries(f repository.SpanFilter, _ int) ([]repository.TraceSummaryRow, error) {
	r.reads <- f
	return nil, nil
}

func (r recordingSearch) ExcludedOperations(int64) ([]string, error) { return nil, nil }

func TestWarmTraceListsLoopReadsAtStartAndEndsWithItsContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := recordingSearch{reads: make(chan repository.SpanFilter, 8)}
	qs := service.NewQueryService(rec, nil, nil)
	projects := fakeProjectLister{list: []repository.Project{{ID: 4}}}

	ctx, cancel := context.WithCancel(context.Background())
	WarmTraceListsLoop(ctx, qs, projects, logger)

	for _, want := range []int64{0, 4} {
		select {
		case f := <-rec.reads:
			if f.ProjectID != want {
				t.Errorf("read project = %d, want %d", f.ProjectID, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no read for project %d after the loop started", want)
		}
	}
	cancel()
}

func TestWarmTraceListsLoopWithoutServiceOrProjectsDoesNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	WarmTraceListsLoop(context.Background(), nil, fakeProjectLister{}, logger)
	WarmTraceListsLoop(context.Background(), service.NewQueryService(recordingSearch{}, nil, nil), nil, logger)
}
