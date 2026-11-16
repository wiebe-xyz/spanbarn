package engine

import (
	"context"
	"testing"
)

// One bucket of one operation holds a row per environment, and the same
// environment still has one row. The migration runs again without error.
func TestAggregatesEnvironmentMigration(t *testing.T) {
	db := spansWriter(t)
	ctx := context.Background()
	insert := func(env string) error {
		_, err := db.ExecContext(ctx, `INSERT INTO aggregates
			(project_id, environment, service, operation, resource, kind, bucket, count)
			VALUES (1, ?, 'web', 'GET /', '', 'server', '2026-11-16 12:00:00', 1)`, env)
		return err
	}
	for _, env := range []string{"production", "staging", ""} {
		if err := insert(env); err != nil {
			t.Fatalf("insert %q: %v", env, err)
		}
	}
	if err := insert("staging"); err == nil {
		t.Error("a second staging row for the same bucket was accepted")
	}
	var old int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_agg_lookup'`).Scan(&old); err != nil || old != 0 {
		t.Errorf("old unique index still present (count %d, err %v)", old, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := spansAggregatesEnvironmentUp(ctx, tx); err != nil {
		t.Fatalf("second run: %v", err)
	}
}
