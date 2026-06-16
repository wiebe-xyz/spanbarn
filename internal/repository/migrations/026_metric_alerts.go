package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up026, down026)
}

func up026(ctx context.Context, tx *sql.Tx) error {
	// Extend alerts with metric-threshold fields. Existing latency/error_rate
	// alerts leave these empty.
	stmts := []string{
		`ALTER TABLE alerts ADD COLUMN metric_name TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alerts ADD COLUMN metric_agg TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE alerts ADD COLUMN label_filters TEXT NOT NULL DEFAULT '{}'`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down026(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE alerts DROP COLUMN metric_name`,
		`ALTER TABLE alerts DROP COLUMN metric_agg`,
		`ALTER TABLE alerts DROP COLUMN label_filters`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
