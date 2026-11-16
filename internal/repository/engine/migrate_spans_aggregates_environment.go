package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// spansAggregatesEnvironmentUp gives aggregates an environment (#558, slice
// 2b). One project now holds the staging and the production traffic of a
// product, and a bucket row that sums both cannot be read back per
// environment. environment is part of the row's key: the unique index that
// the upsert targets moves from (project_id, service, ...) to
// (project_id, environment, service, ...).
//
// The column is NOT NULL DEFAULT ”: ADD COLUMN then only rewrites the schema,
// existing rows read ” (an environment the sender never named, which reads
// as production), and an older binary that does not name the column keeps
// inserting. The old unique index goes because it would refuse the second
// environment's row of the same bucket. An older binary's upsert names the old
// conflict target, which no longer exists, so after a rollback past this
// migration aggregate writes fail until the roll forward. Aggregates back the
// alerts, the usage figures and the read that rollups do not cover.
//
// The new index is built over the table once per file. aggregates keeps 30
// days of one row per bucket, service and operation.
func spansAggregatesEnvironmentUp(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE aggregates ADD COLUMN environment TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("aggregates environment: %w", err)
	}
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agg_lookup_env
			ON aggregates(project_id, environment, service, operation, resource, kind, bucket)`,
		`DROP INDEX IF EXISTS idx_agg_lookup`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("aggregates environment index: %w", err)
		}
	}
	return nil
}
