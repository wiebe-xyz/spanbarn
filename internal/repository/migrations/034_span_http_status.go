package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up034, down034)
}

// up034 exposes the HTTP status code of a span as an indexed column so the
// dashboard can count spans per status code without parsing the attributes JSON
// of every row in the window.
//
// The column is a VIRTUAL generated column derived from attributes, not a value
// the ingest path writes. Spans reach the table through three writers
// (InsertSpansContext, the staging insert and CommitStagingFlush), and a stored
// column would have to be wired into all three: a writer that forgets it leaves
// the column NULL and the status-code chart empty while everything reads back
// fine. A generated column cannot drift from attributes, and it also covers rows
// written before this migration.
//
// Both semantic-convention spellings are read, newest first: the stable
// http.response.status_code, then the legacy http.status_code. Spans without
// either get NULL, and so do spans whose attributes are not valid JSON. The
// json_valid guard matters: the column is evaluated on every insert (the index
// covers it), and a bare json_extract on malformed text raises an error that
// would fail the whole insert batch.
//
// ADD COLUMN of a VIRTUAL generated column is metadata-only (no row rewrite).
// The index is not: building it evaluates json_extract over every existing span
// while holding the single write connection, which is the wedge migrations 030
// and 032 avoid. Spans are short-lived (retention keeps the table small; prod
// held about 87k rows when this shipped), so the build takes well under a
// second and the migration runs as is. Do not create the column or the index by
// hand ahead of the deploy: the ALTER below is not guarded, so a pre-existing
// column fails the migration with "duplicate column name".
func up034(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE spans ADD COLUMN http_status INTEGER GENERATED ALWAYS AS (
			CASE WHEN json_valid(attributes) THEN
				COALESCE(
					json_extract(attributes, '$."http.response.status_code"'),
					json_extract(attributes, '$."http.status_code"')
				)
			END
		) VIRTUAL`,
		`CREATE INDEX IF NOT EXISTS idx_spans_http_status ON spans(project_id, ingested_at, http_status)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down034(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_spans_http_status`,
		`ALTER TABLE spans DROP COLUMN http_status`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
