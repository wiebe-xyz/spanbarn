package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func heatmapQuery() HeatmapQuery {
	now := time.Now().UTC()
	return HeatmapQuery{ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour), TimeBuckets: 4, DurationBuckets: 3}
}

func heatmapSpan(i int, project int64, name string, start time.Time, durationUs int64) repository.Span {
	s := healthSpan(fmt.Sprint("t", i), fmt.Sprint("s", i), "", name)
	s.ProjectID = project
	s.StartTimeUs = start.UnixMicro()
	s.DurationUs = durationUs
	return s
}

func TestHeatmapValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	noProject := heatmapQuery()
	noProject.ProjectID = 0
	reversed := heatmapQuery()
	reversed.From, reversed.To = now, now.Add(-time.Hour)
	tooWide := heatmapQuery()
	tooWide.From = now.Add(-30 * 24 * time.Hour)
	badFilter := heatmapQuery()
	badFilter.Filter = &filter.Expr{Filters: []filter.Node{{Key: "a", Op: "~", Value: "b"}}}

	for name, q := range map[string]HeatmapQuery{"project": noProject, "reversed": reversed, "too wide": tooWide} {
		if _, err := svc.Heatmap(context.Background(), q); !errors.Is(err, ErrInvalidAttributeRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidAttributeRequest", name, err)
		}
	}
	if _, err := svc.Heatmap(context.Background(), badFilter); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("bad filter: err = %v, want filter.ErrInvalid", err)
	}
}

func TestHeatmapBucketsAndFilter(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	q := heatmapQuery()
	start := q.From.Add(5 * time.Minute)
	spans := []repository.Span{
		heatmapSpan(0, 1, "GET /a", start, 100),
		heatmapSpan(1, 1, "GET /a", start, 1_000_000),
		heatmapSpan(2, 1, "GET /b", start, 100),
		heatmapSpan(3, 2, "GET /a", start, 100),
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Heatmap(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 3 || res.Capped || res.TimeBuckets != 4 || res.DurationBuckets != 3 || len(res.DurationEdgesUs) != 4 {
		t.Fatalf("result = %+v", res)
	}
	var total int64
	for _, c := range res.Cells {
		total += c.Count
	}
	if total != 3 {
		t.Errorf("cells hold %d spans, want 3 (project 2 excluded)", total)
	}

	q.Filter = &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "name", Op: filter.OpEq, Value: "GET /a"}}}
	res, err = svc.Heatmap(context.Background(), q)
	if err != nil || res.Scanned != 2 {
		t.Fatalf("filtered: %+v %v", res, err)
	}
}

func TestHeatmapEmptyRangeHasNoEdges(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	res, err := svc.Heatmap(context.Background(), heatmapQuery())
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 0 || res.Cells == nil || res.DurationEdgesUs == nil || len(res.Cells) != 0 || len(res.DurationEdgesUs) != 0 {
		t.Errorf("empty result = %+v, want non-nil empty slices", res)
	}
}

func TestHeatmapCapFlagAndHardCaps(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	q := heatmapQuery()
	var spans []repository.Span
	for i := 0; i < 6; i++ {
		spans = append(spans, heatmapSpan(i, 1, "GET /a", q.From.Add(time.Minute), int64(100+i)))
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	q.MaxSpans = 3
	res, err := svc.Heatmap(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped || res.Scanned != 3 || res.MaxSpans != 3 {
		t.Errorf("capped result = %+v", res)
	}

	q.MaxSpans, q.TimeBuckets, q.DurationBuckets = 10_000_000, 10_000, 10_000
	res, err = svc.Heatmap(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxSpans != hardHeatmapMaxSpans || res.TimeBuckets != hardHeatmapTimeBuckets || res.DurationBuckets != hardHeatmapDurationBuckets {
		t.Errorf("hard caps not applied: %+v", res)
	}

	q.MaxSpans, q.TimeBuckets, q.DurationBuckets = 0, 0, 0
	res, err = svc.Heatmap(context.Background(), q)
	if err != nil || res.MaxSpans != defaultHeatmapMaxSpans || res.TimeBuckets != defaultHeatmapTimeBuckets {
		t.Errorf("defaults not applied: %+v %v", res, err)
	}
}
