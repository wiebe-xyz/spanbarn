package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(up018, down018)
}

// up018 enables incremental auto-vacuum. Freed pages from subsequent retention
// deletes will be returned to the OS by PRAGMA incremental_vacuum in the
// checkpoint loop (db.go). A full VACUUM is intentionally omitted here: on a
// large existing database it takes many minutes and blocks all startup; the DB
// will be compacted naturally once retention has removed the bulk of aggregate
// data and the file is small enough for VACUUM to complete quickly.
func up018(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL")
	return err
}

func down018(_ context.Context, _ *sql.DB) error {
	return nil
}
