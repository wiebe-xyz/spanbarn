package repository

import (
	"context"
	"strings"
	"testing"
)

// traceIndexSeek is how a plan reads idx_spans_trace once it covers the
// trace's structure columns.
const traceIndexSeek = "COVERING INDEX idx_spans_trace (trace_id=? AND project_id=?)"

// Every query that pins idx_spans_trace reads it as a covering index after
// the rebuild, so none fetches span rows from the table for the trace lookup.
func TestTraceIndexPinsReadCoveringIndex(t *testing.T) {
	repo := setupTestDB(t)
	queries := map[string]struct {
		sql  string
		args []any
	}{
		"refresh":     {refreshTraceStructureSQL, []any{1, "x"}},
		"spanless":    {deleteSpanlessSummarySQL, []any{1, "x"}},
		"boring warm": {boringSummaryWarmSQL, []any{1, "x"}},
		"evict chunk": {evictRowsSQL("spans"), []any{`["a"]`, 1, "2030-01-01", evictChunk}},
	}
	for name, q := range queries {
		plan := queryPlan(t, repo, q.sql, q.args...)
		if !strings.Contains(plan, traceIndexSeek) || strings.Contains(plan, "SCAN spans") {
			t.Errorf("%s does not read idx_spans_trace as a covering index:\n%s", name, plan)
		}
	}
}

// The migration rebuilds an index that holds only trace_id, under the same
// name, and leaves a covering one alone.
func TestSpansTraceIndexMigrationRebuildsOnce(t *testing.T) {
	repo := setupTestDB(t)
	db := repo.writer(FamilySpans)
	ctx := context.Background()
	for _, stmt := range []string{`DROP INDEX idx_spans_trace`, `CREATE INDEX idx_spans_trace ON spans(trace_id)`} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	run := func() {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := spansTraceIndexCoveringUp(ctx, tx); err != nil {
			t.Fatalf("migration: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	schemaVersion := func() (v int64) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	run()
	var ddl string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_spans_trace'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "parent_span_id") {
		t.Fatalf("index not rebuilt: %s", ddl)
	}

	before := schemaVersion()
	run()
	if after := schemaVersion(); after != before {
		t.Fatalf("second run changed the schema (%d -> %d); a covering index must be left alone", before, after)
	}
}
