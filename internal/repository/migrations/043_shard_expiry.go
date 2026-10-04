package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up043, down043)
}

// up043 prepares shard expiry. retiring_at records when the writer marked a
// shard retiring; its file is deleted five minutes later, once no reader pool
// lists it.
//
// kept_logs holds the logs of error-sampled and pinned traces copied out of a
// logs shard before its file is deleted. It has the columns of logs in the
// same order, so the logs read view unions it with SELECT *. id is the
// source row's id (unique across shards), so copying a file twice inserts
// nothing the second time.
func up043(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE shards ADD COLUMN retiring_at DATETIME`,
		`CREATE TABLE kept_logs (
			id                      INTEGER PRIMARY KEY,
			project_id              INTEGER NOT NULL,
			trace_id                TEXT,
			span_id                 TEXT,
			severity_number         INTEGER  NOT NULL DEFAULT 0,
			severity_text           TEXT     NOT NULL DEFAULT '',
			time_unix_nano          INTEGER  NOT NULL,
			observed_time_unix_nano INTEGER  NOT NULL DEFAULT 0,
			body                    TEXT     NOT NULL DEFAULT '',
			attributes              TEXT     NOT NULL DEFAULT '{}',
			ingested_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX idx_kept_logs_project_ingested ON kept_logs(project_id, ingested_at)`,
		`CREATE INDEX idx_kept_logs_trace ON kept_logs(trace_id)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down043(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP TABLE kept_logs`,
		`ALTER TABLE shards DROP COLUMN retiring_at`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
