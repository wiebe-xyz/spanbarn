package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up035, down035)
}

// up035 adds the structural columns of a trace to trace_summaries:
//
//   - has_root:     1 when the trace holds a span without a parent, 0 when it
//     does not, NULL while unknown.
//   - orphan_count: spans whose parent_span_id has no matching span_id in the
//     same trace, NULL while unknown.
//
// Before this, an empty root_name was the only hint that a trace had no root,
// and finding rootless traces meant joining back to spans.
//
// Backfill is lazy and the migration itself touches no rows. ADD COLUMN with no
// default is metadata-only, so existing rows read back NULL ("not yet
// computed"). Migration 032 has no backfill because a GROUP BY over spans held
// the single write connection and wedged the writer, so this one runs no
// statement over spans and none over every summary. Two things fill the
// columns:
//
//  1. New and updated summaries are computed inline in the ingest transaction
//     (upsertTraceSummariesTx), per trace, from idx_spans_trace.
//  2. The retention worker calls BackfillTraceStructure each cycle. It reads
//     NULL rows through the partial index below, in small batches that each
//     take the write connection once and release it, capped per cycle.
//
// Until a row is backfilled it matches neither the has_root=0 nor the
// orphan_count>0 filter, so a filter never reports a guess.
//
// The partial index only holds NULL rows. Creating it reads trace_summaries once
// (one narrow row per trace, never spans). On a very large table, create it by
// hand before the deploy; IF NOT EXISTS then makes this statement a no-op.
func up035(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE trace_summaries ADD COLUMN has_root INTEGER`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE trace_summaries ADD COLUMN orphan_count INTEGER`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_trace_summaries_unchecked
		ON trace_summaries(project_id, trace_id) WHERE has_root IS NULL`)
	return err
}

func down035(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_trace_summaries_unchecked`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE trace_summaries DROP COLUMN orphan_count`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE trace_summaries DROP COLUMN has_root`)
	return err
}
