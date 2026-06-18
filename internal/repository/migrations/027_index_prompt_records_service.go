package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up027, down027)
}

// up027 adds a covering index for the prompts page's service-filtered query:
//
//	WHERE project_id = ? AND service = ? AND ingested_at BETWEEN ? AND ?
//	ORDER BY ingested_at DESC
//
// The existing idx_prompt_records_service is (project_id, service, name,
// start_time_us) — it has no ingested_at, so a service-filtered prompts query
// could not use the index for the time-window range or the ORDER BY and fell
// back to a scan + sort. prompt_records is a small table, so building this
// index is cheap (unlike the spans-table index removed in migration 022).
func up027(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_prompt_records_project_service_ingested ON prompt_records(project_id, service, ingested_at)`)
	return err
}

func down027(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_prompt_records_project_service_ingested`)
	return err
}
