package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up021, down021)
}

// up021 adds a covering index for the boring-trace retention subquery:
//
//	SELECT trace_id FROM spans
//	WHERE ingested_at < ?
//	GROUP BY trace_id
//	HAVING MAX(CASE WHEN status IN ('error',...) THEN 1 ELSE 0 END) = 0
//	   AND MAX(duration_us) <= ?
//	LIMIT 100
//
// Without this index SQLite must read every span row (including large
// attributes/events columns) to evaluate the GROUP BY and HAVING. With a
// covering index on (ingested_at, trace_id, status, duration_us) the inner
// query is answered entirely from the index — no table row lookups needed.
func up021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_spans_boring_trace
		ON spans(ingested_at, trace_id, status, duration_us)`)
	return err
}

func down021(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_spans_boring_trace`)
	return err
}
