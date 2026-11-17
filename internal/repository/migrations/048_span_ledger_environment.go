package migrations

import (
	"context"
	"database/sql"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up048, down048)
}

// up048 gives the span ledger an environment (#558): its key becomes (bucket,
// project_id, environment), so two environments of one project in one bucket are
// two rows and keep their loss apart. SQLite cannot change a primary key in
// place, so the table is rebuilt: the rows are copied with environment ” (which
// a production read selects), the old table is dropped and the new one renamed.
// The ledger keeps 14 days of 5-minute buckets, so the copy is small. A database
// whose version rows were rewound already has the column and is left alone.
func up048(ctx context.Context, tx *sql.Tx) error {
	has, err := spanLedgerHasEnvironment(ctx, tx)
	if err != nil || has {
		return err
	}
	for _, stmt := range []string{
		`CREATE TABLE span_ledger_env (
			bucket         INTEGER NOT NULL,
			project_id     INTEGER NOT NULL,
			environment    TEXT    NOT NULL DEFAULT '',
			accepted       INTEGER NOT NULL DEFAULT 0,
			sampled_out    INTEGER NOT NULL DEFAULT 0,
			shed_unsampled INTEGER NOT NULL DEFAULT 0,
			lost_in_buffer INTEGER NOT NULL DEFAULT 0,
			forwarded      INTEGER NOT NULL DEFAULT 0,
			processed      INTEGER NOT NULL DEFAULT 0,
			failed         INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket, project_id, environment)
		) WITHOUT ROWID`,
		`INSERT INTO span_ledger_env
			(bucket, project_id, environment, accepted, sampled_out, shed_unsampled,
			 lost_in_buffer, forwarded, processed, failed)
			SELECT bucket, project_id, '', accepted, sampled_out, shed_unsampled,
			 lost_in_buffer, forwarded, processed, failed FROM span_ledger`,
		`DROP TABLE span_ledger`,
		`ALTER TABLE span_ledger_env RENAME TO span_ledger`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// spanLedgerHasEnvironment reports whether span_ledger already has the column.
func spanLedgerHasEnvironment(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info('span_ledger')`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(name, "environment") {
			return true, nil
		}
	}
	return false, rows.Err()
}

// down048 folds the environments back into one row per bucket and project.
func down048(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE span_ledger_old (
			bucket         INTEGER NOT NULL,
			project_id     INTEGER NOT NULL,
			accepted       INTEGER NOT NULL DEFAULT 0,
			sampled_out    INTEGER NOT NULL DEFAULT 0,
			shed_unsampled INTEGER NOT NULL DEFAULT 0,
			lost_in_buffer INTEGER NOT NULL DEFAULT 0,
			forwarded      INTEGER NOT NULL DEFAULT 0,
			processed      INTEGER NOT NULL DEFAULT 0,
			failed         INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket, project_id)
		) WITHOUT ROWID`,
		`INSERT INTO span_ledger_old
			SELECT bucket, project_id, SUM(accepted), SUM(sampled_out), SUM(shed_unsampled),
			 SUM(lost_in_buffer), SUM(forwarded), SUM(processed), SUM(failed)
			FROM span_ledger GROUP BY bucket, project_id`,
		`DROP TABLE span_ledger`,
		`ALTER TABLE span_ledger_old RENAME TO span_ledger`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
