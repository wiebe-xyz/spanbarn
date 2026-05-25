package migrations

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationNoTxContext(up018, down018)
}

// up018 enables incremental auto-vacuum so the retention worker's batch deletes
// can gradually return freed pages to the OS via PRAGMA incremental_vacuum. The
// setting only takes effect after a full VACUUM, which also compacts the existing
// file. VACUUM is run here as a one-time operation; it may take several minutes
// on a large database but is safe to interrupt (a partial VACUUM leaves the DB
// unchanged). If it fails due to insufficient disk space the migration still
// succeeds — auto_vacuum will activate on the next manual VACUUM.
func up018(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		slog.Warn("018: VACUUM failed — auto_vacuum will activate on next manual VACUUM", "error", err)
	}
	return nil
}

func down018(_ context.Context, _ *sql.DB) error {
	return nil
}
