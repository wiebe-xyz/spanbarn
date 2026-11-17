package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up049, down049)
}

// up049 adds the merge ledger of #558. One row per source, target, family,
// table and shard records how many rows a merge moved and the last id it
// moved, so a merge that stops can say where it stood and a second run adds to
// the same counts. The row with an empty table name is the run itself. A new
// table in the main database, so an older image ignores it after a rollback.
func up049(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS project_merges (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id     INTEGER NOT NULL,
			target_id     INTEGER NOT NULL,
			environment   TEXT    NOT NULL DEFAULT '',
			family        TEXT    NOT NULL DEFAULT '',
			table_name    TEXT    NOT NULL DEFAULT '',
			shard         TEXT    NOT NULL DEFAULT '',
			cursor        INTEGER NOT NULL DEFAULT 0,
			rows_moved    INTEGER NOT NULL DEFAULT 0,
			rows_collided INTEGER NOT NULL DEFAULT 0,
			state         TEXT    NOT NULL DEFAULT 'running',
			started_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE (source_id, target_id, family, table_name, shard)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_project_merges_source ON project_merges(source_id)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down049(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS project_merges`)
	return err
}
