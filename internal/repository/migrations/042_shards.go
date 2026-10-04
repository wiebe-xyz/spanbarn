package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up042, down042)
}

// up042 adds the shards table: one row per time-shard file of a heavy family
// (logs, metrics, prompts), written by the writer's shard manager.
//
// file is the name inside $SPANBARN_DB_PATH.d, so a database moved to another
// directory keeps finding its shards. period_start and period_end are UTC
// dates (YYYY-MM-DD), end exclusive. state is 'active' or 'retiring'.
func up042(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE shards (
		family       TEXT NOT NULL,
		period_start TEXT NOT NULL,
		period_end   TEXT NOT NULL,
		file         TEXT NOT NULL UNIQUE,
		state        TEXT NOT NULL DEFAULT 'active',
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (family, period_start)
	)`)
	return err
}

func down042(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE shards`)
	return err
}
