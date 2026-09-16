package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up033, down033)
}

// up033 adds the coarse rollup tiers that bound metric storage by resolution
// instead of by window length.
//
// metric_rollups holds 5-minute buckets keyed by the FULL attribute set, so its
// row count follows cardinality rather than time: production reached 5.5M rows /
// 3.7 GB (76% of the database) in 30 days, one project contributing 10,483
// attribute sets per metric because service.version and service.instance.id are
// part of the key and every deploy mints new series. Shortening the window is no
// answer — it drops the history and leaves the same growth rate — so older data
// is compacted into progressively coarser buckets (1h, 1d, 1w, 1mo) with the
// volatile attributes dropped at each step.
//
// The coarse tiers live in their own table rather than as a `step` column on
// metric_rollups. Adding the column is free, but putting it in that table's
// UNIQUE key would rebuild an index over 5.5M rows, and this migration has to
// run on a volume that is already 95% full. Creating empty tables and one
// ALTER TABLE ADD COLUMN (O(1) in SQLite) costs nothing.
//
// No backfill here: internal/rollup's compactor fills the tiers forward, oldest
// bucket first, deleting each source window as it goes so that the operation
// nets free space on a full disk. metric_rollup_compaction records how far each
// tier has been compacted, which is what makes deleting the source tier safe:
// retention never drops rows past the watermark, at any pressure level.
func up033(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS metric_rollups_coarse (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id       INTEGER NOT NULL,
			name             TEXT    NOT NULL,
			type             TEXT    NOT NULL,
			unit             TEXT    NOT NULL DEFAULT '',
			temporality      TEXT    NOT NULL DEFAULT '',
			attr_fingerprint TEXT    NOT NULL,
			attributes       TEXT    NOT NULL DEFAULT '{}',
			step_seconds     INTEGER NOT NULL,
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
		// The compactor's upsert target: one row per series per tier bucket.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_metric_rollups_coarse_lookup
			ON metric_rollups_coarse(project_id, name, attr_fingerprint, step_seconds, bucket)`,
		// Serves the series query (name + range at one resolution).
		`CREATE INDEX IF NOT EXISTS idx_metric_rollups_coarse_name
			ON metric_rollups_coarse(project_id, name, step_seconds, bucket)`,
		// Serves insights (all names for a project) and per-tier retention, which
		// deletes per project so each batch seeks instead of scanning.
		`CREATE INDEX IF NOT EXISTS idx_metric_rollups_coarse_bucket
			ON metric_rollups_coarse(project_id, step_seconds, bucket)`,
		// How far each tier has been compacted into the tier above it. Written by
		// the compactor, read by retention before it deletes anything.
		`CREATE TABLE IF NOT EXISTS metric_rollup_compaction (
			step_seconds      INTEGER PRIMARY KEY,
			compacted_through DATETIME NOT NULL,
			updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// OTLP aggregation temporality was never recorded, so a cumulative counter
		// was stored as its running total and a delta counter as a lone increment,
		// with no way to tell them apart. The compactor has to know which it is
		// before it can turn a tier into per-bucket increases. Empty means
		// "unknown", which the compactor treats as cumulative (the SDK default).
		`ALTER TABLE metric_rollups ADD COLUMN temporality TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down033(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_metric_rollups_coarse_bucket`,
		`DROP INDEX IF EXISTS idx_metric_rollups_coarse_name`,
		`DROP INDEX IF EXISTS idx_metric_rollups_coarse_lookup`,
		`DROP TABLE IF EXISTS metric_rollups_coarse`,
		`DROP TABLE IF EXISTS metric_rollup_compaction`,
		`ALTER TABLE metric_rollups DROP COLUMN temporality`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
