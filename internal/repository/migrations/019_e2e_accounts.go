package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up019, down019)
}

func up019(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		ALTER TABLE projects ADD COLUMN e2e_enabled INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE users    ADD COLUMN e2e_expires_at DATETIME;
	`)
	return err
}

func down019(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		ALTER TABLE projects DROP COLUMN e2e_enabled;
		ALTER TABLE users    DROP COLUMN e2e_expires_at;
	`)
	return err
}
