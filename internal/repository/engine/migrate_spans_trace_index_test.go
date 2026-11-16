package engine

import (
	"context"
	"strings"
	"testing"
)

// The migration rebuilds an index that holds only trace_id, under the same
// name, and leaves a covering one alone.
func TestSpansTraceIndexMigrationRebuildsOnce(t *testing.T) {
	db := spansWriter(t)
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
