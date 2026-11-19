package migrations

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up052, down052)
}

// up052 lets a project name the IamBarn organizations that may read it (#557).
// Empty keeps today's rule: the owner's organization reads it and nobody else
// does. A nullable-free column with an empty default, so an older image after
// a rollback reads the same rows.
func up052(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN organization_ids TEXT NOT NULL DEFAULT ''`)
	if err != nil && strings.Contains(err.Error(), "duplicate column") {
		return nil
	}
	return err
}

func down052(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE projects DROP COLUMN organization_ids`)
	return err
}
