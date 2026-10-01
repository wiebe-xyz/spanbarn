package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up037, down037)
}

// up037 adds boards: an ordered set of query panels per project.
//
//   - saved_queries.definition holds the rest of a query definition (group by,
//     calculations, order, limit, sample, chart calculation) as JSON, next to
//     the filters column of migration 036. An empty value marks a row from
//     before boards, which only carries a filter.
//   - boards holds the project, name, shared time range and refresh interval.
//   - board_panels points a board at a saved query. Position orders the grid.
//   - releases holds the markers drawn on time series panels.
//
// All tables are small (a handful of rows per project), so the indexes only
// serve the per-project and per-board lookups.
func up037(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE saved_queries ADD COLUMN definition TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE boards (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id      INTEGER NOT NULL REFERENCES projects(id),
			name            TEXT NOT NULL,
			time_range      TEXT NOT NULL DEFAULT '24h',
			refresh_seconds INTEGER NOT NULL DEFAULT 0,
			created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX idx_boards_project ON boards(project_id)`,
		`CREATE TABLE board_panels (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			board_id       INTEGER NOT NULL REFERENCES boards(id),
			saved_query_id INTEGER NOT NULL REFERENCES saved_queries(id),
			title          TEXT NOT NULL DEFAULT '',
			view           TEXT NOT NULL DEFAULT 'table',
			position       INTEGER NOT NULL DEFAULT 0,
			created_at     DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX idx_board_panels_board ON board_panels(board_id, position)`,
		`CREATE INDEX idx_board_panels_query ON board_panels(saved_query_id)`,
		`CREATE TABLE releases (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id  INTEGER NOT NULL REFERENCES projects(id),
			version     TEXT NOT NULL,
			released_at DATETIME NOT NULL,
			created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE INDEX idx_releases_project_time ON releases(project_id, released_at)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down037(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`DROP TABLE IF EXISTS releases`,
		`DROP TABLE IF EXISTS board_panels`,
		`DROP TABLE IF EXISTS boards`,
		`ALTER TABLE saved_queries DROP COLUMN definition`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
