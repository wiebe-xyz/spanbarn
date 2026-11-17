package engine

import (
	"context"
	"testing"
)

// span_edges as it was before the environment column.
const oldSpanEdgesDDL = `CREATE TABLE span_edges (
	step_seconds INTEGER NOT NULL, bucket INTEGER NOT NULL, project_id INTEGER NOT NULL,
	source TEXT NOT NULL, target TEXT NOT NULL, target_type TEXT NOT NULL,
	count INTEGER NOT NULL, error_count INTEGER NOT NULL, weighted_count INTEGER NOT NULL,
	weighted_spans INTEGER NOT NULL, sum_duration_us INTEGER NOT NULL, min_us INTEGER NOT NULL,
	max_us INTEGER NOT NULL, hist BLOB NOT NULL,
	PRIMARY KEY (step_seconds, bucket, project_id, source, target, target_type)
) WITHOUT ROWID`

// A file at the old shape keeps every row, markers included, with
// environment ”, a second run changes nothing, and the new key holds one row
// per environment.
func TestSpanEdgesEnvironmentMigrationCopiesRows(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	for _, q := range []string{`DROP TABLE span_edges`, oldSpanEdgesDDL,
		`INSERT INTO span_edges VALUES (300, 1000, 1, 'web', 'db', 'database', 4, 1, 40, 4, 100, 10, 50, x'0102')`,
		`INSERT INTO span_edges VALUES (300, 1000, 0, '', '', '', 0, 0, 0, 0, 0, 0, 0, x'00')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for run := 1; run <= 2; run++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := spansSpanEdgesEnvironmentUp(ctx, tx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var rows, empty, count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(environment = ''), SUM(count) FROM span_edges`).Scan(&rows, &empty, &count); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || empty != 2 || count != 4 {
		t.Errorf("rows=%d empty-environment=%d count=%d, want 2 2 4", rows, empty, count)
	}
	insert := `INSERT INTO span_edges VALUES (300, 1000, 1, ?, 'web', 'db', 'database', 1, 0, 1, 1, 10, 10, 10, x'00')`
	if _, err := db.ExecContext(ctx, insert, "staging"); err != nil {
		t.Errorf("staging edge after the migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, insert, "staging"); err == nil {
		t.Error("a second staging edge for the same bucket was accepted")
	}
}

func TestSpanEdgesEnvironmentMigrationRefusesAFileWithoutTheTable(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TABLE span_edges`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := spansSpanEdgesEnvironmentUp(ctx, tx); err == nil {
		t.Fatal("migration passed on a file with no span_edges table")
	}
}
