package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// Migration 048 rebuilds span_ledger with the environment in its key. The rows
// written before it keep their counts under environment ”, and a second run on
// a database that has the column changes nothing.
func TestSpanLedgerEnvironmentMigrationCopiesRows(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := Migrate(h.DB); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(h.DB, ".", 47); err != nil {
		t.Fatalf("down to 47: %v", err)
	}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO span_ledger
		(bucket, project_id, accepted, processed, failed) VALUES (1800000000, 7, 10, 8, 2), (1800000300, 7, 5, 5, 0)`); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	if err := Migrate(h.DB); err != nil {
		t.Fatalf("up: %v", err)
	}
	var rows, accepted, processed int64
	var env string
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*), SUM(accepted), SUM(processed), MIN(environment) FROM span_ledger`).
		Scan(&rows, &accepted, &processed, &env); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || accepted != 15 || processed != 13 || env != "" {
		t.Errorf("rows %d accepted %d processed %d environment %q, want 2 rows, 15, 13 and ''", rows, accepted, processed, env)
	}

	// Two environments of one project and bucket are two rows.
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO span_ledger (bucket, project_id, environment, accepted)
		VALUES (1800000000, 7, 'staging', 3)`); err != nil {
		t.Fatalf("a second environment in the same bucket: %v", err)
	}

	if err := Migrate(h.DB); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// Rolling the migration back folds the environments into one row per bucket and
// project, so the older image's key holds.
func TestSpanLedgerEnvironmentMigrationDownFoldsEnvironments(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := Migrate(h.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO span_ledger (bucket, project_id, environment, accepted, processed)
		VALUES (1800000000, 7, '', 4, 4), (1800000000, 7, 'staging', 3, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := goose.DownTo(h.DB, ".", 47); err != nil {
		t.Fatal(err)
	}
	var rows, accepted, processed int64
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*), SUM(accepted), SUM(processed) FROM span_ledger`).
		Scan(&rows, &accepted, &processed); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || accepted != 7 || processed != 5 {
		t.Errorf("rows %d accepted %d processed %d, want 1 row, 7, 5", rows, accepted, processed)
	}
}
