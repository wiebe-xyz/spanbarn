package repository

import (
	"context"
	"testing"
	"time"
)

// TestSampleWeightThroughStaging checks that a span's weight survives the
// staging table and the move into spans, and that an unknown weight is stored
// as NULL and reads back as 0.
func TestSampleWeightThroughStaging(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	weighted := stagingSpan("traceW", "w1", "unset")
	weighted.SampleWeight = 50
	unknown := stagingSpan("traceW", "w2", "unset")
	if err := repo.InsertSpansStaging(ctx, []Span{weighted, unknown}); err != nil {
		t.Fatalf("InsertSpansStaging: %v", err)
	}

	got, err := repo.GetStagingSpansByTraceIDs(ctx, []string{"traceW"})
	if err != nil {
		t.Fatalf("GetStagingSpansByTraceIDs: %v", err)
	}
	weights := map[string]int{}
	for _, s := range got {
		weights[s.SpanID] = s.SampleWeight
	}
	if weights["w1"] != 50 || weights["w2"] != 0 {
		t.Fatalf("staged weights = %v, want w1=50 w2=0", weights)
	}

	if err := repo.CommitStagingFlush(ctx, []string{"traceW"}, got); err != nil {
		t.Fatalf("CommitStagingFlush: %v", err)
	}
	var nulls, sum int64
	if err := repo.DB().QueryRow(`SELECT COUNT(*) - COUNT(sample_weight), COALESCE(SUM(sample_weight), 0) FROM spans WHERE trace_id = 'traceW'`).Scan(&nulls, &sum); err != nil {
		t.Fatalf("read spans: %v", err)
	}
	if nulls != 1 || sum != 50 {
		t.Fatalf("spans: %d NULL weights and sum %d, want 1 and 50", nulls, sum)
	}

	stored, err := repo.GetSpansForAggregation(time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("GetSpansForAggregation: %v", err)
	}
	var total int
	for _, s := range stored {
		total += s.SampleWeight
	}
	if len(stored) != 2 || total != 50 {
		t.Fatalf("retention read %d spans with weight %d, want 2 and 50", len(stored), total)
	}
}

// TestAggregateWeightsUpsert checks that the weighted columns add up across
// upserts, and that a bucket first written without them (NULL, as an older
// binary or a row from before the migration leaves it) stays unknown.
func TestAggregateWeightsUpsert(t *testing.T) {
	repo := setupTestDB(t)
	bucket := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	agg := Aggregate{
		ProjectID: 1, Service: "api", Operation: "GET /", Bucket: bucket,
		Count: 3, ErrorCount: 1, WeightedCount: 201, WeightedSpans: 3,
	}
	if err := repo.UpsertAggregates([]Aggregate{agg}); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}
	if err := repo.UpsertAggregate(agg); err != nil {
		t.Fatalf("UpsertAggregate: %v", err)
	}

	// The same upsert without the weighted columns, the way the previous
	// release writes it.
	if _, err := repo.writer(FamilySpans).Exec(`INSERT INTO aggregates
		(project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us)
		VALUES (1, 'api', 'GET /old', '', '', ?, 5, 0, 0, 0, 0, 0, 0)`, bucket); err != nil {
		t.Fatalf("insert unweighted row: %v", err)
	}
	if err := repo.UpsertAggregate(Aggregate{
		ProjectID: 1, Service: "api", Operation: "GET /old", Bucket: bucket,
		Count: 2, WeightedCount: 2, WeightedSpans: 2,
	}); err != nil {
		t.Fatalf("UpsertAggregate onto unweighted row: %v", err)
	}

	got, err := repo.QueryAggregates(AggregateFilter{ProjectID: 1, From: bucket, To: bucket})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	byOp := map[string]Aggregate{}
	for _, a := range got {
		byOp[a.Operation] = a
	}

	if n, ok := byOp["GET /"].WeightedEstimate(); !ok || n != 402 {
		t.Errorf("weighted bucket: WeightedEstimate = %d, %v, want 402, true", n, ok)
	}
	old := byOp["GET /old"]
	if old.Count != 7 {
		t.Errorf("unweighted bucket count = %d, want 7", old.Count)
	}
	if _, ok := old.WeightedEstimate(); ok {
		t.Errorf("a bucket that started without weights must stay unknown, got %+v", old)
	}
}

func TestWeightedEstimate(t *testing.T) {
	cases := []struct {
		name string
		a    Aggregate
		want int64
		ok   bool
	}{
		{"every span weighted", Aggregate{Count: 3, WeightedCount: 1001, WeightedSpans: 3}, 1001, true},
		{"some spans unknown", Aggregate{Count: 3, WeightedCount: 1000, WeightedSpans: 1}, 0, false},
		{"no weights", Aggregate{Count: 3}, 0, false},
		{"empty", Aggregate{}, 0, false},
	}
	for _, c := range cases {
		n, ok := c.a.WeightedEstimate()
		if n != c.want || ok != c.ok {
			t.Errorf("%s: WeightedEstimate = %d, %v, want %d, %v", c.name, n, ok, c.want, c.ok)
		}
	}
}
