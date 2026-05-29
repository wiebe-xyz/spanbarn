package migrations

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up020, down020)
}

// up020 ensures users.e2e_expires_at exists. Migration 019 used a single
// multi-statement ExecContext call; some SQLite driver versions only execute
// the first statement, leaving e2e_expires_at missing. This migration adds the
// column idempotently.
func up020(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE users ADD COLUMN e2e_expires_at DATETIME`)
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil // already present — 019 ran both statements correctly
	}
	return err
}

func down020(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE users DROP COLUMN e2e_expires_at`)
	return err
}
