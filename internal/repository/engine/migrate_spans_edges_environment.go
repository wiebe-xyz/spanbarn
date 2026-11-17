package engine

import (
	"context"
	"database/sql"
)

// spansSpanEdgesEnvironmentUp gives span_edges an environment (#558, slice
// 2b), the way spansSpanRollupsEnvironmentUp does for span_rollups: the
// primary key gains environment right after project_id, so the table is built
// again (span_edges_env), filled with an empty environment (calls from spans
// that carry none, which read as production), dropped and renamed, in the
// migration's transaction. A bucket marker keeps an empty environment: it says
// a writer covered the bucket, for every environment. A file that already has
// the column is left alone.
//
// An older binary's upsert names the old conflict target, so after a rollback
// past this migration span edge writes fail until the roll forward. The
// accumulator keeps the unwritten slots for an hour and compaction waits on
// its watermark. The table holds one row per call pair and bucket and the
// tiers keep it small next to span_rollups.
func spansSpanEdgesEnvironmentUp(ctx context.Context, tx *sql.Tx) error {
	if todo, err := spansTableLacksEnvironment(ctx, tx, "span edges environment", "span_edges"); err != nil || !todo {
		return err
	}
	return execSpansDDL(ctx, tx, "span edges environment",
		`DROP TABLE IF EXISTS span_edges_env`,
		`CREATE TABLE span_edges_env (
			step_seconds    INTEGER NOT NULL,
			bucket          INTEGER NOT NULL,
			project_id      INTEGER NOT NULL,
			environment     TEXT NOT NULL DEFAULT '',
			source          TEXT NOT NULL,
			target          TEXT NOT NULL,
			target_type     TEXT NOT NULL,
			count           INTEGER NOT NULL,
			error_count     INTEGER NOT NULL,
			weighted_count  INTEGER NOT NULL,
			weighted_spans  INTEGER NOT NULL,
			sum_duration_us INTEGER NOT NULL,
			min_us          INTEGER NOT NULL,
			max_us          INTEGER NOT NULL,
			hist            BLOB NOT NULL,
			PRIMARY KEY (step_seconds, bucket, project_id, environment, source, target, target_type)
		) WITHOUT ROWID`,
		`INSERT INTO span_edges_env (step_seconds, bucket, project_id, environment, source, target, target_type,
			count, error_count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist)
		SELECT step_seconds, bucket, project_id, '', source, target, target_type,
			count, error_count, weighted_count, weighted_spans, sum_duration_us, min_us, max_us, hist
		FROM span_edges ORDER BY step_seconds, bucket, project_id, source, target, target_type`,
		`DROP TABLE span_edges`,
		`ALTER TABLE span_edges_env RENAME TO span_edges`)
}
