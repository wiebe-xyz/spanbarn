package engine

import (
	"context"
	"database/sql"
	"testing"
)

// oldSpanRollupsDDL is span_rollups as it was before the environment column.
const oldSpanRollupsDDL = `CREATE TABLE span_rollups (
	step_seconds INTEGER NOT NULL, bucket INTEGER NOT NULL, project_id INTEGER NOT NULL,
	service TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, is_root INTEGER NOT NULL,
	status TEXT NOT NULL, http_status INTEGER NOT NULL, count INTEGER NOT NULL,
	weighted_count INTEGER NOT NULL, weighted_spans INTEGER NOT NULL, sum_duration_us INTEGER NOT NULL,
	min_us INTEGER NOT NULL, max_us INTEGER NOT NULL, hist BLOB NOT NULL,
	PRIMARY KEY (step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status)
) WITHOUT ROWID`

func insertSpanRollup(ctx context.Context, db *sql.DB, env string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO span_rollups
		(step_seconds, bucket, project_id, environment, service, name, kind, is_root, status, http_status,
		 count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist)
		VALUES (300, 1000, 1, ?, 'web', 'GET /', 'server', 1, 'ok', 0, 1, 1, 1, 10, 10, 10, x'00')`, env)
	return err
}

// One bucket of one operation holds a row per environment, and the same
// environment still has one row.
func TestSpanRollupsHoldOneRowPerEnvironment(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	for _, env := range []string{"production", "staging", ""} {
		if err := insertSpanRollup(ctx, db, env); err != nil {
			t.Fatalf("insert %q: %v", env, err)
		}
	}
	if err := insertSpanRollup(ctx, db, "staging"); err == nil {
		t.Error("a second staging row for the same bucket was accepted")
	}
}

// A file at the old shape keeps every row, with environment ”, and a second
// run changes nothing.
func TestSpanRollupsEnvironmentMigrationCopiesRows(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	for _, q := range []string{`DROP TABLE span_rollups`, oldSpanRollupsDDL,
		`INSERT INTO span_rollups VALUES (300, 1000, 1, 'web', 'GET /', 'server', 1, 'ok', 0, 4, 40, 4, 100, 10, 50, x'0102')`,
		`INSERT INTO span_rollups VALUES (3600, 3600, 2, 'api', 'job', 'internal', 0, 'error', 500, 1, 1, 1, 7, 7, 7, x'03')`,
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
		if err := spansSpanRollupsEnvironmentUp(ctx, tx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var rows, empty, count, httpStatus int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(environment = ''), SUM(count), SUM(http_status) FROM span_rollups`).
		Scan(&rows, &empty, &count, &httpStatus); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || empty != 2 || count != 5 || httpStatus != 500 {
		t.Errorf("rows=%d empty-environment=%d count=%d http=%d, want 2 2 5 500", rows, empty, count, httpStatus)
	}
	var leftover int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'span_rollups_env'`).Scan(&leftover); err != nil || leftover != 0 {
		t.Errorf("staging table left behind (count %d, err %v)", leftover, err)
	}
	if err := insertSpanRollup(ctx, db, "staging"); err != nil {
		t.Errorf("staging row after the migration: %v", err)
	}
}

// A file without a span_rollups table is not a spans file: the migration says
// so instead of passing.
func TestSpanRollupsEnvironmentMigrationRefusesAFileWithoutTheTable(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TABLE span_rollups`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := spansSpanRollupsEnvironmentUp(ctx, tx); err == nil {
		t.Fatal("migration passed on a file with no span_rollups table")
	}
}
