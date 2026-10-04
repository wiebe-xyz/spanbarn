package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up041, down041)
}

// up041 adds the durable flag to spans and trace_summaries (issue #171).
//
// A durable trace is the first clean trace of a (project, operation) hour. It
// skips the short boring window, retention copies its spans to error_samples at
// the interesting cutoff, and its summary is kept until the error cutoff, so a
// job that runs once a day stays readable for as long as its errors would.
//
// ADD COLUMN with a constant default only rewrites the schema, so this applies
// to a populated database without a table rebuild. No index: retention already
// reads spans in ingested_at order and checks the flag per row.
func up041(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`ALTER TABLE spans ADD COLUMN durable INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE trace_summaries ADD COLUMN durable INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down041(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`ALTER TABLE trace_summaries DROP COLUMN durable`,
		`ALTER TABLE spans DROP COLUMN durable`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
