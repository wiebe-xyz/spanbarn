package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up022, down022)
}

// up022 is a no-op: the boring-span cleanup DELETE
// (ingested_at < ? AND status NOT IN (...) AND duration_us < ?)
// is already served efficiently by idx_spans_ingested on (ingested_at),
// which lets SQLite skip over old spans without a full table scan.
// The dedicated 3-column covering index was removed to avoid blocking
// pod startup for 30+ minutes on large databases (12M+ rows).
func up022(ctx context.Context, tx *sql.Tx) error {
	return nil
}

func down022(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_boring_cleanup`)
	return err
}
