package migrations

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up047, down047)
}

// up047 gives an ingest key an environment (#558): the fallback stamped on a
// record whose own resource carries no deployment.environment. A constant
// default makes this a metadata change in SQLite, and an older image that
// selects named columns ignores it after a rollback. A database whose version
// rows were rewound (the 045 migration test does this) already has the column,
// so a duplicate is not an error.
func up047(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE api_keys ADD COLUMN environment TEXT NOT NULL DEFAULT ''`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}

func down047(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE api_keys DROP COLUMN environment`)
	return err
}
