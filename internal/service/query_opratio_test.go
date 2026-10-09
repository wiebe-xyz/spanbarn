package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// opRatioLookup mimics the ingest lookup: the operation key wins over the
// project ratio.
type opRatioLookup struct {
	project int
	ops     map[string]int
}

func (l opRatioLookup) Ratio(_ context.Context, _ int64, operation string) int {
	if r, ok := l.ops[operation]; ok {
		return r
	}
	return l.project
}

func TestQueryCountsScaleByOperationRatio(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, opRatioLookup{project: 1000, ops: map[string]int{"rare": 1}})

	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	for _, a := range []repository.Aggregate{
		{ProjectID: 1, Service: "web", Operation: "GET /", Bucket: bucket, Count: 10, P50Us: 1000, P95Us: 1000, P99Us: 1000},
		{ProjectID: 1, Service: "web", Operation: "rare", Bucket: bucket, Count: 5, P50Us: 1000, P95Us: 1000, P99Us: 1000},
	} {
		if err := repo.UpsertAggregate(a); err != nil {
			t.Fatal(err)
		}
	}
	from, to := bucket.Add(-time.Hour), bucket.Add(time.Hour)
	ctx := context.Background()

	t.Run("services weight per operation", func(t *testing.T) {
		got, err := svc.ListServices(ctx, 1, from, to, false)
		if err != nil || len(got) != 1 {
			t.Fatalf("ListServices = %+v, %v", got, err)
		}
		if got[0].SpanCount != 10*1000+5 {
			t.Errorf("SpanCount = %d, want 10005", got[0].SpanCount)
		}
	})

	t.Run("operations use their own ratio", func(t *testing.T) {
		got, err := svc.ListOperations(ctx, 1, "web", from, to, "")
		if err != nil || len(got) != 2 {
			t.Fatalf("ListOperations = %+v, %v", got, err)
		}
		counts := map[string]int64{}
		for _, o := range got {
			counts[o.Operation] = o.SpanCount
		}
		if counts["rare"] != 5 || counts["GET /"] != 10000 {
			t.Errorf("counts = %v", counts)
		}
	})

	t.Run("timeseries uses the operation ratio", func(t *testing.T) {
		rare, err := svc.GetTimeseries(ctx, 1, "web", "rare", from, to, time.Hour)
		if err != nil || len(rare) != 1 || rare[0].Count != 5 {
			t.Fatalf("rare timeseries = %+v, %v", rare, err)
		}
		common, err := svc.GetTimeseries(ctx, 1, "web", "GET /", from, to, time.Hour)
		if err != nil || len(common) != 1 || common[0].Count != 10000 {
			t.Fatalf("common timeseries = %+v, %v", common, err)
		}
	})

	t.Run("trace groups use the root operation ratio", func(t *testing.T) {
		mk := func(trace, span, name string) repository.Span {
			return repository.Span{
				ProjectID: 1, TraceID: trace, SpanID: span, Name: name, Service: "web",
				Resource: "/", Kind: "server", Status: "ok", StartTimeUs: time.Now().UnixMicro(),
				DurationUs: 1000, Attributes: `{}`, Events: `[]`,
			}
		}
		if err := repo.InsertSpans([]repository.Span{mk("t1", "s1", "GET /"), mk("t2", "s2", "rare"), mk("t3", "s3", "rare")}); err != nil {
			t.Fatal(err)
		}
		groups, err := svc.ListTraceGroups(ctx, TraceSearchFilter{ProjectID: 1, To: time.Now().Add(time.Hour)})
		if err != nil || len(groups) != 2 {
			t.Fatalf("ListTraceGroups = %+v, %v", groups, err)
		}
		counts := map[string]int64{}
		for _, g := range groups {
			counts[g.Operation] = g.Count
		}
		if counts["rare"] != 2 || counts["GET /"] != 1000 {
			t.Errorf("counts = %v", counts)
		}
	})
}
