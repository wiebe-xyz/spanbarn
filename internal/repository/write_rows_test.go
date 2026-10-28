package repository

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// recordJobRows installs an in-memory tracer provider and returns a func that
// lists the write.rows of each writescheduler.job span by write.op, -1 for a
// job span without write.rows.
func recordJobRows(t *testing.T) func() map[string][]int64 {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})
	return func() map[string][]int64 {
		_ = tp.ForceFlush(context.Background())
		out := map[string][]int64{}
		for _, s := range sr.Ended() {
			if s.Name() != "writescheduler.job" {
				continue
			}
			op, rows := "", writescheduler.NoRows
			for _, a := range s.Attributes() {
				switch a.Key {
				case "write.op":
					op = a.Value.AsString()
				case "write.rows":
					rows = a.Value.AsInt64()
				}
			}
			out[op] = append(out[op], rows)
		}
		return out
	}
}

// The writes that hold the writer for seconds report how many rows they
// wrote, under the labels they had before, so a slow job's time can be read
// per row. A retention delete through execLowAffecting reports the rows it
// deleted.
func TestSlowWriteOpsReportRows(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	now := time.Now().UTC().Truncate(time.Minute)
	if err := repo.UpsertAggregates([]Aggregate{
		{ProjectID: 1, Service: "svc", Operation: "a", Kind: "internal", Bucket: now, Count: 1},
		{ProjectID: 1, Service: "svc", Operation: "b", Kind: "internal", Bucket: now, Count: 1},
	}); err != nil {
		t.Fatalf("UpsertAggregates: %v", err)
	}
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	if err := repo.UpsertMetricRollups([]MetricRollup{fineRow(day, "fp1", 1), fineRow(day, "fp2", 1), fineRow(day, "fp3", 1)}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{coarseRow(3600, day, 1), coarseRow(86400, day, 1)}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}
	staged := []Span{tsSpan("S", "s1", "", "root", "svc", "ok", 1000, 50), tsSpan("S", "s2", "s1", "child", "svc", "ok", 1000, 20)}
	for i := range staged {
		staged[i].IngestedAt = now
	}
	if err := repo.InsertSpansStaging(ctx, staged); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// One span of the two moves to spans; both staged rows go.
	if err := repo.CommitStagingFlush(ctx, []string{"S"}, staged[:1]); err != nil {
		t.Fatalf("CommitStagingFlush: %v", err)
	}
	if _, err := repo.DeleteSpansByMaxID(1 << 62); err != nil {
		t.Fatalf("DeleteSpansByMaxID: %v", err)
	}

	got := jobRows()
	want := map[string]int64{
		"UpsertAggregates":    2,
		"UpsertMetricRollups": 3,
		"UpsertCoarseRollups": 2,
		"CommitStagingFlush":  3,
		spanDeleteLabel:       1,
	}
	for op, rows := range want {
		if len(got[op]) != 1 || got[op][0] != rows {
			t.Errorf("%s job rows = %v, want [%d]", op, got[op], rows)
		}
	}
}

// The boring-span expiry's delete job reports the spans it deleted.
func TestBoringExpiryReportsRows(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	insertBoringSpans(t, repo, 1, 3, time.Now().Add(-2*time.Hour).UTC())
	if _, err := repo.DeleteExpiredBoringSpans(ctx, time.Now()); err != nil {
		t.Fatalf("DeleteExpiredBoringSpans: %v", err)
	}
	if got := jobRows()[boringExpiryLabel]; len(got) != 1 || got[0] != 3 {
		t.Errorf("%s job rows = %v, want [3]", boringExpiryLabel, got)
	}
}

// The spanless summaries of one expiry chunk are dropped boringSummaryChunk
// traces per job, and each job reports the summaries it deleted.
func TestBoringSummariesRunInChunks(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	traces := 2*boringSummaryChunk + 50
	insertBoringSpans(t, repo, 1, traces, time.Now().Add(-2*time.Hour).UTC())
	if _, err := repo.DeleteExpiredBoringSpans(ctx, time.Now()); err != nil {
		t.Fatalf("DeleteExpiredBoringSpans: %v", err)
	}
	got := jobRows()[boringSummaryLabel]
	if !slices.Equal(got, []int64{boringSummaryChunk, boringSummaryChunk, 50}) {
		t.Errorf("%s job rows = %v, want [%d %d 50]", boringSummaryLabel, got, boringSummaryChunk, boringSummaryChunk)
	}
	if n := len(summaryIDs(t, repo)); n != 0 {
		t.Errorf("%d summaries left, want 0", n)
	}
}

// The span rollup and span edge writers name their chunk jobs after the
// caller and report each chunk's rows. The stack-derived label named the loop
// body of the shared generic writer, so all four read as "]-range1".
func TestTierWritesNameTheirJobs(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	b := time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC)
	var rollups []SpanRollup
	for i := range spanRollupChunk + 7 {
		rollups = append(rollups, spanRollupRow(b, fmt.Sprintf("op-%04d", i), int64(i+1)))
	}
	edge := []SpanEdge{spanEdgeRow(b, 1, "api", "sqlite", 5)}
	for _, err := range []error{
		repo.AddSpanRollups(rollups),
		repo.ReplaceSpanRollups(ctx, rollups[:1]),
		repo.AddSpanEdges(edge),
		repo.ReplaceSpanEdges(ctx, edge),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := jobRows()
	want := map[string][]int64{
		"AddSpanRollups":     {spanRollupChunk, 7},
		"ReplaceSpanRollups": {1},
		"AddSpanEdges":       {1},
		"ReplaceSpanEdges":   {1},
	}
	for op, rows := range want {
		if !slices.Equal(got[op], rows) {
			t.Errorf("%s job rows = %v, want %v", op, got[op], rows)
		}
	}
	for op := range got {
		if strings.Contains(op, "range") {
			t.Errorf("job labelled %q", op)
		}
	}
}
