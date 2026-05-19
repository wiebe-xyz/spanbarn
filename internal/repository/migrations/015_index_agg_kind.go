package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up015, down015)
}

// up015 adds a covering index on (project_id, kind, bucket) for the aggregates
// table. ListServices with server_only=true queries aggregates without a service
// predicate, so the existing idx_agg_lookup(project_id, service, …, kind, bucket)
// cannot be used to filter by kind efficiently. This index makes the kind-filtered
// services query use a narrow index scan instead of a full table scan.
func up015(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_agg_kind_bucket
		 ON aggregates(project_id, kind, bucket)`)
	return err
}

func down015(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_agg_kind_bucket`)
	return err
}
