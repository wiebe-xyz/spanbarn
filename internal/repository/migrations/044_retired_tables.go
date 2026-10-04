package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up044, down044)
}

// up044 records the heavy tables main is retiring. Once shards take a
// family's inserts and row retention has emptied main's copy of its table,
// the writer lists the table here; readers leave it out of the family view on
// their next refresh, and five minutes later the writer drops it.
func up044(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE retired_tables (
		name       TEXT PRIMARY KEY,
		retired_at DATETIME NOT NULL
	)`)
	return err
}

func down044(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS retired_tables`)
	return err
}
