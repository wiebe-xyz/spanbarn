package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up032, down032)
}

// up032 adds trace_summaries: one row per (project_id, trace_id) holding
// everything the trace-LIST needs (root name/service/duration, start time, span
// count, has_error). The list query previously did a GROUP BY over every span in
// the whole time window (spans ∪ error_samples) before applying LIMIT, which
// timed out (30s+) on busy projects. Reading a pre-rolled summary turns that into
// an indexed ORDER BY … LIMIT scan, independent of span volume.
//
// Maintained by the staging flush (and the inline insert when staging is off) via
// an UPSERT, so a trace's summary lands atomically with its spans. Cleaned up by
// retention in lockstep with the spans it describes: boring-sampled traces carry
// an expires_at and are dropped early; interesting traces are dropped at the
// interesting cutoff; error traces persist to the error cutoff (mirroring
// error_samples), so the list keeps showing errors exactly as long as before.
//
// No backfill: a GROUP BY over the existing spans table at migration time would
// hold the single write connection and wedge the writer mid-deploy (the failure
// mode migration 030's notes call out). The table fills forward from flushes.
func up032(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS trace_summaries (
			project_id       INTEGER  NOT NULL,
			trace_id         TEXT     NOT NULL,
			root_name        TEXT     NOT NULL DEFAULT '',
			root_service     TEXT     NOT NULL DEFAULT '',
			root_duration_us INTEGER  NOT NULL DEFAULT 0,
			start_time_us    INTEGER  NOT NULL DEFAULT 0,
			span_count       INTEGER  NOT NULL DEFAULT 0,
			has_error        INTEGER  NOT NULL DEFAULT 0,
			ingested_at      DATETIME NOT NULL,
			expires_at       DATETIME,
			PRIMARY KEY (project_id, trace_id)
		)`,
		// Serves the list: filter by project + ingested_at window, order by
		// ingested_at DESC. ingested_at is server-authoritative (robust against
		// client clock skew) and matches the retention/cleanup key.
		`CREATE INDEX IF NOT EXISTS idx_trace_summaries_list ON trace_summaries(project_id, ingested_at DESC)`,
		// Early cleanup of boring-sampled summaries seeks this partial index, same
		// pattern as idx_spans_expires.
		`CREATE INDEX IF NOT EXISTS idx_trace_summaries_expires ON trace_summaries(expires_at) WHERE expires_at IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down032(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_trace_summaries_expires`,
		`DROP INDEX IF EXISTS idx_trace_summaries_list`,
		`DROP TABLE IF EXISTS trace_summaries`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
