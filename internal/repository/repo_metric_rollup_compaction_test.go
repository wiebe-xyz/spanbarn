package repository

import (
	"context"
	"strings"
	"testing"
	"time"
)

func fineRow(bucket time.Time, fingerprint string, last float64) MetricRollup {
	return MetricRollup{
		ProjectID: 1, Name: "reqs", Type: "sum", Unit: "1", Temporality: "cumulative",
		AttrFingerprint: fingerprint, Attributes: `{"service.name":"api"}`,
		Bucket: bucket.UTC(), Count: 1, Last: last,
	}
}

// TestRollupWindowReturnsTheCarryRow: compaction asks for one source bucket
// before the window it is building, because a cumulative counter's increase
// inside the window can only be measured against the sample before it.
func TestRollupWindowReturnsTheCarryRow(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	hour := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	if err := repo.UpsertMetricRollups([]MetricRollup{
		fineRow(hour.Add(-5*time.Minute), "fp", 90), // carry
		fineRow(hour, "fp", 100),
		fineRow(hour.Add(30*time.Minute), "fp", 130),
		fineRow(hour.Add(time.Hour), "fp", 200), // next window, excluded
	}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}

	got, err := repo.RollupWindow(ctx, Rollup5mStep, hour.Add(-5*time.Minute), hour.Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("RollupWindow: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3 (the carry row plus the window, exclusive of the next bucket)", len(got))
	}
	if got[0].Last != 90 || got[2].Last != 130 {
		t.Errorf("rows are not in bucket order: %v", []float64{got[0].Last, got[1].Last, got[2].Last})
	}
	if got[0].Temporality != "cumulative" {
		t.Errorf("temporality = %q, want it to survive the round trip", got[0].Temporality)
	}
}

// TestRollupWindowReadsTheRequestedTier: the 5-minute tier lives in
// metric_rollups and every coarser tier in metric_rollups_coarse, and a
// compaction pass must see only the tier it is reading from.
func TestRollupWindowReadsTheRequestedTier(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	if err := repo.UpsertMetricRollups([]MetricRollup{fineRow(day, "fp", 1)}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{
		coarseRow(3600, day, 2),
		coarseRow(86400, day, 3),
	}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	got, err := repo.RollupWindow(ctx, 3600, day, day.Add(24*time.Hour), 0)
	if err != nil {
		t.Fatalf("RollupWindow: %v", err)
	}
	if len(got) != 1 || got[0].Sum != 2 {
		t.Fatalf("got %d rows (%+v), want only the hourly one", len(got), got)
	}
}

// TestRollupWindowQueriesSeekAnIndex is a performance assertion, and it exists
// because the performance is a correctness problem in disguise.
//
// The first version filtered on bucket alone. Every index on these tables leads
// with project_id, so SQLite had nothing to seek and scanned the whole table:
// 5.5M rows and 2.6 GB on production, on the single writer connection, which
// stalled every other write behind each compacted bucket until the write
// scheduler reported a wedge. A row-count test passes either way, so the query
// plan is what has to be checked.
func TestRollupWindowQueriesSeekAnIndex(t *testing.T) {
	repo := setupTestDB(t)

	tests := []struct {
		name string
		sql  string
		args []any
	}{
		{"5m tier", rollupWindowFineQuery, []any{1, time.Now(), time.Now(), 10}},
		{"coarse tier", rollupWindowCoarseQuery, []any{1, 3600, time.Now(), time.Now(), 10}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.DB().Query("EXPLAIN QUERY PLAN "+tc.sql, tc.args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			defer rows.Close()

			var plan []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatalf("scan: %v", err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v", err)
			}
			if len(plan) == 0 {
				t.Fatal("no query plan returned")
			}

			for _, step := range plan {
				if strings.HasPrefix(step, "SCAN ") {
					t.Errorf("query plan scans a table instead of seeking an index: %q\nfull plan: %v", step, plan)
				}
			}
			if !strings.Contains(strings.Join(plan, " "), "USING INDEX") {
				t.Errorf("query plan uses no index: %v", plan)
			}
		})
	}
}

// TestCoarseLastByFingerprint feeds the running total that keeps a compacted
// counter monotonic from one tier bucket to the next.
func TestCoarseLastByFingerprint(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	hour := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

	rows := []MetricRollup{coarseRow(3600, hour, 10), coarseRow(3600, hour, 20)}
	rows[1].AttrFingerprint = "fp2"
	if err := repo.UpsertCoarseRollups(ctx, rows); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}

	got, err := repo.CoarseLastByFingerprint(ctx, 1, 3600, hour)
	if err != nil {
		t.Fatalf("CoarseLastByFingerprint: %v", err)
	}
	if len(got) != 2 || got["fp"] != 10 || got["fp2"] != 20 {
		t.Errorf("got %v, want fp=10 and fp2=20", got)
	}

	empty, err := repo.CoarseLastByFingerprint(ctx, 1, 3600, hour.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CoarseLastByFingerprint (empty bucket): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %v for a bucket with no rows, want nothing", empty)
	}
}
