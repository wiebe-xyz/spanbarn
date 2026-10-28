package repository

import (
	"context"
	"database/sql"
)

// spansSpanEdgesUp adds span_edges: the calls one service made to one target
// per (step, bucket, project), with counts, sample weights and a
// quarter-octave duration histogram. The span_rollups writer fills it from
// every span it receives, and the span_rollups compactor rolls it from 5
// minutes to hours to days under the same watermarks.
//
// target_type is "service" for a call into another instrumented service and
// the dependency kind (database, http, rpc, ...) for a client span. Every
// bucket the writer flushed holds a marker row with empty source, target and
// target_type, so a bucket without calls reads apart from a bucket nobody
// wrote.
//
// The table starts empty: nothing is backfilled from spans.
func spansSpanEdgesUp(ctx context.Context, tx *sql.Tx) error {
	return execSpansDDL(ctx, tx, "span edges", `CREATE TABLE IF NOT EXISTS span_edges (
		step_seconds    INTEGER NOT NULL,
		bucket          INTEGER NOT NULL,
		project_id      INTEGER NOT NULL,
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
		PRIMARY KEY (step_seconds, bucket, project_id, source, target, target_type)
	) WITHOUT ROWID`)
}
