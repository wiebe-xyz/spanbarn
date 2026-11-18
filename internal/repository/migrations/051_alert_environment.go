package migrations

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up051, down051)
}

// up051 gives an alert an environment (#329). The project merge of #558 put the
// production, staging and testing aggregates of one service into one project,
// and an alert summed all of them. The default is empty, which keeps every
// existing alert evaluating all environments, so an older image after a
// rollback reads the same rows. A run over a database that has the column is a
// no-op.
func up051(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE alerts ADD COLUMN environment TEXT NOT NULL DEFAULT ''`)
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	return err
}

func down051(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE alerts DROP COLUMN environment`)
	return err
}
