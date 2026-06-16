package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up025, down025)
}

func up025(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		// metric_rollups stores per-series, per-bucket downsampled metrics so that
		// long-range queries do not scan the raw metrics table. One row per
		// (project, name, attribute set, time bucket). Columns are type-agnostic:
		//   gauge        — sum/count gives the bucket average; min/max/last carry detail
		//   sum          — last is the cumulative value at bucket end (rate derived across buckets)
		//   histogram    — extra holds merged {bounds,counts}; obs_count/sum the totals
		//   exp_histogram— extra holds the last merged exponential buckets
		//   summary      — extra holds the last quantiles
		`CREATE TABLE metric_rollups (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id       INTEGER NOT NULL,
			name             TEXT    NOT NULL,
			type             TEXT    NOT NULL,
			unit             TEXT    NOT NULL DEFAULT '',
			attr_fingerprint TEXT    NOT NULL,
			attributes       TEXT    NOT NULL DEFAULT '{}',
			bucket           DATETIME NOT NULL,
			count            INTEGER NOT NULL DEFAULT 0,
			sum              REAL    NOT NULL DEFAULT 0,
			min              REAL    NOT NULL DEFAULT 0,
			max              REAL    NOT NULL DEFAULT 0,
			last             REAL    NOT NULL DEFAULT 0,
			obs_count        INTEGER NOT NULL DEFAULT 0,
			extra            TEXT,
			ingested_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE UNIQUE INDEX idx_metric_rollups_lookup ON metric_rollups(project_id, name, attr_fingerprint, bucket)`,
		`CREATE INDEX idx_metric_rollups_bucket ON metric_rollups(project_id, bucket)`,
		`CREATE INDEX idx_metric_rollups_name ON metric_rollups(project_id, name, bucket)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down025(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS metric_rollups`)
	return err
}
