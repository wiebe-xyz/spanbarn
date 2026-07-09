package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up029, down029)
}

// up029 creates the spans_staging table: a cheap landing zone the writer appends
// every incoming span to, decoupling the fast Redis drain from the expensive
// indexed write. A background flush groups staging rows by trace, classifies the
// complete trace, moves only the interesting ones into the indexed `spans` table,
// and deletes the processed rows.
//
// Staging carries a SINGLE index — on trace_id — which the flush uses to group
// ready traces, gather a whole trace's spans for classification, and delete by
// trace on cleanup. No other indexes, so appends stay ~11x cheaper than the main
// spans table. Time ordering comes free from the implicit rowid (inserts are
// monotonic), which the age-based flush and the hard-age GC backstop scan by.
func up029(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS spans_staging (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id INTEGER NOT NULL,
			trace_id TEXT NOT NULL,
			span_id TEXT NOT NULL,
			parent_span_id TEXT,
			name TEXT NOT NULL,
			service TEXT NOT NULL,
			resource TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'internal',
			status TEXT NOT NULL DEFAULT 'unset',
			start_time_us INTEGER NOT NULL,
			duration_us INTEGER NOT NULL,
			attributes TEXT NOT NULL DEFAULT '{}',
			events TEXT NOT NULL DEFAULT '[]',
			ingested_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_spans_staging_trace ON spans_staging(trace_id)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down029(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS spans_staging`)
	return err
}
