package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up040, down040)
}

// up040 adds calculated fields: a named expression per project that filters and
// group-bys use like a span column or attribute key (internal/calcfield).
//
// The table holds a handful of rows per project and is read once per query, so
// the unique index on (project_id, name) is the only index it needs.
func up040(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`CREATE TABLE calculated_fields (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL REFERENCES projects(id),
			name       TEXT NOT NULL,
			expression TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE UNIQUE INDEX idx_calculated_fields_project_name ON calculated_fields(project_id, name)`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down040(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS calculated_fields`)
	return err
}
