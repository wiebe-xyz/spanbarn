package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up053, down053)
}

// up053 adds the two tables behind the rejected-key counters (#572).
// key_rejections holds one row per key fingerprint that was refused: how many
// requests, and when the first and the newest came. revoked_keys remembers the
// fingerprint, id, project and name of every key revoked from now on, so a
// refused fingerprint can be told from a key that never existed. Keys revoked
// before this migration read as unknown.
func up053(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS key_rejections (
			fingerprint TEXT PRIMARY KEY,
			count       INTEGER NOT NULL DEFAULT 0,
			first_seen  INTEGER NOT NULL,
			last_seen   INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_key_rejections_last_seen ON key_rejections(last_seen)`,
		`CREATE TABLE IF NOT EXISTS revoked_keys (
			fingerprint TEXT PRIMARY KEY,
			key_id      INTEGER NOT NULL,
			project_id  INTEGER NOT NULL,
			name        TEXT NOT NULL DEFAULT '',
			revoked_at  INTEGER NOT NULL
		)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down053(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{`DROP TABLE IF EXISTS revoked_keys`, `DROP TABLE IF EXISTS key_rejections`} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
