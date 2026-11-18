package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up051, down051)
}

// up051 gives an alert an environment (#329). The project merge of #558 put the
// production, staging and testing aggregates of one service into one project,
// and an alert summed all of them. The default is empty, which keeps every
// existing alert evaluating all environments, so an older image after a
// rollback reads the same rows.
func up051(ctx context.Context, tx *sql.Tx) error {
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('alerts') WHERE name = 'environment'`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE alerts ADD COLUMN environment TEXT NOT NULL DEFAULT ''`)
	return err
}

func down051(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE alerts DROP COLUMN environment`)
	return err
}
