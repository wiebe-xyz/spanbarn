package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up028, down028)
}

// up028 drops two span indexes that are strict prefixes of existing composite
// indexes, so every span INSERT was maintaining redundant B-trees:
//
//   - idx_spans_project_id (project_id)  ⊂ idx_spans_project_ingested (project_id, ingested_at)
//   - idx_spans_ingested   (ingested_at) ⊂ idx_spans_project_stats    (ingested_at, project_id, status)
//
// Verified on production via EXPLAIN QUERY PLAN that the affected project_id and
// ingested_at (retention/query) predicates fall back to the composites as
// covering-index searches — no full scans. Removing them cuts per-insert write
// amplification on the hot spans table. DROP INDEX is a metadata/free-page
// operation, so it does not scan the table and is safe during a rolling deploy.
func up028(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_spans_project_id`,
		`DROP INDEX IF EXISTS idx_spans_ingested`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down028(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_spans_project_id ON spans(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_spans_ingested ON spans(ingested_at)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
