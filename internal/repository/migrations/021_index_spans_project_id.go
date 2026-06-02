package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up021, down021)
}

// up021 adds an index on spans(project_id) so targeted deletes and
// per-project queries use an index lookup rather than a full table scan.
// On a large DB this makes the difference between seconds and hours.
func up021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_spans_project_id ON spans(project_id)`)
	return err
}

func down021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_project_id`)
	return err
}
