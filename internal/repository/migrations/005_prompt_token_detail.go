package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up005, down005)
}

func up005(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE prompt_records ADD COLUMN cached_input_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE prompt_records ADD COLUMN reasoning_output_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE prompt_records ADD COLUMN input_cost_usd REAL NOT NULL DEFAULT 0`,
		`ALTER TABLE prompt_records ADD COLUMN output_cost_usd REAL NOT NULL DEFAULT 0`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down005(ctx context.Context, tx *sql.Tx) error {
	return nil // SQLite does not support DROP COLUMN in older versions; columns are benign
}
