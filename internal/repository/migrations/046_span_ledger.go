package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up046, down046)
}

// up046 adds the span ledger: per project and 5-minute bucket, how many spans
// were accepted and how many left through each exit (#471). A new table in the
// main database, so an older image ignores it after a rollback.
func up046(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS span_ledger (
		bucket         INTEGER NOT NULL,
		project_id     INTEGER NOT NULL,
		accepted       INTEGER NOT NULL DEFAULT 0,
		sampled_out    INTEGER NOT NULL DEFAULT 0,
		shed_unsampled INTEGER NOT NULL DEFAULT 0,
		lost_in_buffer INTEGER NOT NULL DEFAULT 0,
		forwarded      INTEGER NOT NULL DEFAULT 0,
		processed      INTEGER NOT NULL DEFAULT 0,
		failed         INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (bucket, project_id)
	) WITHOUT ROWID`)
	return err
}

func down046(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS span_ledger`)
	return err
}
