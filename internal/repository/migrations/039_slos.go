package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up039, down039)
}

// up039 adds service level objectives.
//
//   - slos holds the objective: the good and total filters (filter-model JSON),
//     the target as a fraction between 0 and 1 and the window in days.
//   - slo_burn_alerts holds the burn-rate alerts of an SLO. Webhook, email and
//     cooldown mirror the alerts table. firing marks an alert that has crossed
//     its burn rate and not yet recovered.
//   - slo_counts holds good and total event counts per time bucket, already
//     corrected for the sample ratio. Spans live for the retention window
//     (168h by default) while an SLO window can be 30 days, so the evaluator
//     records counts here each tick and status and burn read them back.
//
// All tables are small, so the only indexes besides the primary keys serve the
// per-project and per-SLO lookups. There is no backfill.
func up039(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE slos (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id   INTEGER NOT NULL REFERENCES projects(id),
			name         TEXT NOT NULL,
			good_filter  TEXT NOT NULL DEFAULT '{}',
			total_filter TEXT NOT NULL DEFAULT '{}',
			target       REAL NOT NULL CHECK (target > 0 AND target < 1),
			window_days  INTEGER NOT NULL CHECK (window_days > 0),
			created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE (project_id, name)
		)`,
		`CREATE INDEX idx_slos_project ON slos(project_id)`,
		`CREATE TABLE slo_burn_alerts (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			slo_id            INTEGER NOT NULL REFERENCES slos(id) ON DELETE CASCADE,
			window_minutes    INTEGER NOT NULL,
			burn_rate         REAL NOT NULL,
			webhook_url       TEXT NOT NULL DEFAULT '',
			email             TEXT NOT NULL DEFAULT '',
			cooldown_minutes  INTEGER NOT NULL DEFAULT 60,
			enabled           INTEGER NOT NULL DEFAULT 1,
			firing            INTEGER NOT NULL DEFAULT 0,
			last_triggered_at DATETIME
		)`,
		`CREATE INDEX idx_slo_burn_alerts_slo ON slo_burn_alerts(slo_id)`,
		`CREATE TABLE slo_counts (
			slo_id       INTEGER NOT NULL REFERENCES slos(id) ON DELETE CASCADE,
			bucket_start DATETIME NOT NULL,
			good         INTEGER NOT NULL,
			total        INTEGER NOT NULL,
			PRIMARY KEY (slo_id, bucket_start)
		)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func down039(ctx context.Context, tx *sql.Tx) error {
	for _, s := range []string{
		`DROP TABLE IF EXISTS slo_counts`,
		`DROP TABLE IF EXISTS slo_burn_alerts`,
		`DROP TABLE IF EXISTS slos`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
