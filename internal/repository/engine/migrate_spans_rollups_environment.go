package engine

import (
	"context"
	"database/sql"
	"fmt"
)

// spanRollupsEnvironmentColumns lists the columns the rebuild copies, in the
// order of the new primary key.
const spanRollupsEnvironmentColumns = `step_seconds, bucket, project_id, environment, service, name, kind, is_root, status, http_status,
			count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist`

// spansSpanRollupsEnvironmentUp gives span_rollups an environment (#558, slice
// 2b). One project now holds the staging and the production traffic of a
// product, and a row that sums both cannot be read back per environment.
// environment joins the primary key, right after project_id, so a time range
// of one project still reads one contiguous slice.
//
// A primary key of a WITHOUT ROWID table cannot change in place, so the
// migration builds the table again: it creates span_rollups_env, copies every
// row into it with an empty environment (spans that carry none, which read as
// production), drops the old table and renames the new one. The copy reads
// the old table in its key order and the new key differs only by a constant
// column, so the inserts append to the end of the tree. The whole rebuild runs
// in the migration's transaction, one write lock for as long as the copy takes.
// The cost follows the size of span_rollups, which the three tiers keep small
// next to spans. A file that already has the column (a re-run) is left alone.
//
// An older binary's upsert names the old conflict target, which no longer
// exists, so after a rollback past this migration span rollup writes fail
// until the roll forward. The accumulator keeps the unwritten slots for an
// hour (spanRollupRequeueAge) and compaction waits on its watermark.
func spansSpanRollupsEnvironmentUp(ctx context.Context, tx *sql.Tx) error {
	if todo, err := spansTableLacksEnvironment(ctx, tx, "span rollups environment", "span_rollups"); err != nil || !todo {
		return err
	}
	return execSpansDDL(ctx, tx, "span rollups environment",
		`DROP TABLE IF EXISTS span_rollups_env`,
		`CREATE TABLE span_rollups_env (
			step_seconds    INTEGER NOT NULL,
			bucket          INTEGER NOT NULL,
			project_id      INTEGER NOT NULL,
			environment     TEXT NOT NULL DEFAULT '',
			service         TEXT NOT NULL,
			name            TEXT NOT NULL,
			kind            TEXT NOT NULL,
			is_root         INTEGER NOT NULL,
			status          TEXT NOT NULL,
			http_status     INTEGER NOT NULL,
			count           INTEGER NOT NULL,
			weighted_count  INTEGER NOT NULL,
			weighted_spans  INTEGER NOT NULL,
			sum_duration_us INTEGER NOT NULL,
			min_us          INTEGER NOT NULL,
			max_us          INTEGER NOT NULL,
			hist            BLOB NOT NULL,
			PRIMARY KEY (step_seconds, bucket, project_id, environment, service, name, kind, is_root, status, http_status)
		) WITHOUT ROWID`,
		`INSERT INTO span_rollups_env (`+spanRollupsEnvironmentColumns+`)
		SELECT step_seconds, bucket, project_id, '', service, name, kind, is_root, status, http_status,
			count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist
		FROM span_rollups ORDER BY step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status`,
		`DROP TABLE span_rollups`,
		`ALTER TABLE span_rollups_env RENAME TO span_rollups`)
}

// spansTableLacksEnvironment reports whether table exists without an
// environment column, which is when a rebuild has work to do. A file without
// the table is not a spans file and is an error, so the migration does not
// pass over it.
func spansTableLacksEnvironment(ctx context.Context, tx *sql.Tx, label, table string) (bool, error) {
	var tables, withColumn int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tables); err != nil {
		return false, fmt.Errorf("%s: %w", label, err)
	}
	if tables == 0 {
		return false, fmt.Errorf("%s: no %s table", label, table)
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'environment'`, table).Scan(&withColumn); err != nil {
		return false, fmt.Errorf("%s: %w", label, err)
	}
	return withColumn == 0, nil
}
