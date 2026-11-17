package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up050, down050)
}

// up050 adds the archive mark of #558. A project whose merge moved every row
// gets archived_at; it stays in the table with its id and slug, so old links
// and keys keep resolving, and it leaves the default project list. A nullable
// column, so an older image after a rollback reads the same rows.
func up050(ctx context.Context, tx *sql.Tx) error {
	// A migration may run again over a database that has the column.
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('projects') WHERE name = 'archived_at'`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN archived_at DATETIME`)
	return err
}

func down050(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE projects DROP COLUMN archived_at`)
	return err
}
