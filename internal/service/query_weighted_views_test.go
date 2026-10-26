package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// TestEstimateSpans checks the raw-span estimate: a set whose every span
// carried a weight counts its weights, any other set scales its ok spans by
// the current rate.
func TestEstimateSpans(t *testing.T) {
	cases := []struct {
		name string
		w    repository.SpanWeights
		want int64
	}{
		{"weighted", repository.SpanWeights{Count: 1002, Spans: 3}, 1002},
		{"no weights", repository.SpanWeights{}, 2001},
		{"partly weighted", repository.SpanWeights{Count: 2, Spans: 2}, 2001},
	}
	for _, c := range cases {
		if got := estimateSpans(3, 1, c.w, 0.001); got != c.want {
			t.Errorf("%s: estimateSpans = %d, want %d", c.name, got, c.want)
		}
	}
}

// weightedSpan is a span the trace buffer kept with the given sample weight.
func weightedSpan(trace, span, parent, name, kind, status, attrs string, weight int) repository.Span {
	return repository.Span{
		ProjectID: 1, TraceID: trace, SpanID: span, ParentSpanID: parent, Name: name, Service: "web",
		Resource: "/", Kind: kind, Status: status, StartTimeUs: time.Now().UnixMicro(), DurationUs: 1000,
		Attributes: attrs, Events: "[]", SampleWeight: weight,
	}
}

// TestRawSpanViewsCountSampleWeights covers every view that counts spans
// straight from the spans table (#333). The project samples 1 in 1000, but
// only one trace was kept by the ratio sample: the other is a floor trace
// (weight 1) and an error trace (weight 1). Each view must count 1000 + 1 + 1
// and leave the ratio out.
func TestRawSpanViewsCountSampleWeights(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, opRatioLookup{project: 1000})
	db := `{"db.system":"postgresql","db.statement":"SELECT 1"}`
	if err := repo.InsertSpans([]repository.Span{
		weightedSpan("t1", "r1", "", "GET /rare", "server", "ok", `{}`, 1000),
		weightedSpan("t1", "c1", "r1", "db.query", "client", "ok", db, 1000),
		weightedSpan("t2", "r2", "", "GET /rare", "server", "ok", `{}`, 1),
		weightedSpan("t2", "c2", "r2", "db.query", "client", "ok", db, 1),
		weightedSpan("t3", "r3", "", "GET /rare", "server", "error", `{}`, 1),
		weightedSpan("t3", "c3", "r3", "db.query", "client", "error", db, 1),
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("trace groups", func(t *testing.T) {
		got, err := svc.ListTraceGroups(ctx, TraceSearchFilter{ProjectID: 1})
		if err != nil || len(got) != 1 {
			t.Fatalf("ListTraceGroups = %+v, %v", got, err)
		}
		if got[0].Count != 1002 || got[0].ErrorCount != 1 {
			t.Errorf("group = %+v, want count 1002 with 1 error", got[0])
		}
	})

	t.Run("database queries", func(t *testing.T) {
		got, _, err := svc.ListDatabaseQueries(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil || len(got) != 1 {
			t.Fatalf("ListDatabaseQueries = %+v, %v", got, err)
		}
		if got[0].CallCount != 1002 {
			t.Errorf("CallCount = %d, want 1002", got[0].CallCount)
		}
	})

	t.Run("dependencies", func(t *testing.T) {
		got, _, err := svc.ListDependencies(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil || len(got) != 1 {
			t.Fatalf("ListDependencies = %+v, %v", got, err)
		}
		if got[0].CallCount != 1002 {
			t.Errorf("CallCount = %d, want 1002", got[0].CallCount)
		}
	})

	// The raw-span fallback covers the last 90 seconds, where the spans above
	// were ingested.
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Minute)

	t.Run("services fallback", func(t *testing.T) {
		got, err := svc.ListServices(ctx, 1, from, to, true)
		if err != nil || len(got) != 1 {
			t.Fatalf("ListServices = %+v, %v", got, err)
		}
		if got[0].SpanCount != 1002 {
			t.Errorf("SpanCount = %d, want 1002", got[0].SpanCount)
		}
	})

	t.Run("operations fallback", func(t *testing.T) {
		got, err := svc.ListOperations(ctx, 1, "web", from, to, "server")
		if err != nil || len(got) != 1 {
			t.Fatalf("ListOperations = %+v, %v", got, err)
		}
		if got[0].SpanCount != 1002 {
			t.Errorf("SpanCount = %d, want 1002", got[0].SpanCount)
		}
	})

	t.Run("timeseries fallback", func(t *testing.T) {
		got, err := svc.GetTimeseries(ctx, 1, "web", "GET /rare", from, to, time.Hour)
		if err != nil {
			t.Fatalf("GetTimeseries: %v", err)
		}
		var total int64
		for _, b := range got {
			total += b.Count
		}
		if total != 1002 {
			t.Errorf("timeseries total = %d, want 1002 (%+v)", total, got)
		}
	})
}
