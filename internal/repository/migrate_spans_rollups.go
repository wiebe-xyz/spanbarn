package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// spansSpanRollupsUp adds span_rollups and its compaction watermarks: span
// counts and a quarter-octave duration histogram per (step, bucket, project,
// service, name, kind, root or not, status, HTTP status), written by the accumulator from
// every span it sees and compacted from 5 minutes to hours to days.
//
// bucket is the start of the bucket in unix seconds, by arrival time
// (ingested_at), the time the dashboard filters on. The key leads with step and
// bucket, so a flush writes next to the previous one at the end of the tree,
// a time range reads one contiguous slice, and retention trims the oldest
// buckets from the front.
//
// http_status is the response code of a root span and 0 for every other span,
// so only root rows split by it.
//
// Both tables start empty: nothing is backfilled from spans.
func spansSpanRollupsUp(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS span_rollups (
			step_seconds    INTEGER NOT NULL,
			bucket          INTEGER NOT NULL,
			project_id      INTEGER NOT NULL,
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
			PRIMARY KEY (step_seconds, bucket, project_id, service, name, kind, is_root, status, http_status)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS span_rollup_compaction (
			step_seconds      INTEGER PRIMARY KEY,
			compacted_through INTEGER NOT NULL
		)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("span rollups: %w", err)
		}
	}
	return nil
}
