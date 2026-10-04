package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// heavyFamily reports whether f is a family whose tables main stops holding
// once shards take its inserts.
func heavyFamily(f Family) bool {
	return f == FamilyLogs || f == FamilyMetrics || f == FamilyPrompts
}

// retireMainTables drops the heavy tables main still holds from before the
// shards, once row retention has emptied them. A table goes in two steps,
// like a shard file: the writer lists it in retired_tables, readers leave it
// out of the family view on their next refresh, and after shardDeleteDelay
// the writer drops it. It returns the number of tables dropped.
func (r *Repository) retireMainTables(ctx context.Context, now time.Time) (int, error) {
	dropped := 0
	for _, f := range r.shards.Families() {
		for _, table := range f.Tables() {
			ok, err := r.retireMainTable(ctx, f, table, now)
			if err != nil {
				return dropped, fmt.Errorf("retire main table %s: %w", table, err)
			}
			if ok {
				dropped++
				r.shards.logger.Info("main table dropped", "table", table)
			}
		}
	}
	return dropped, nil
}

// retireMainTable takes table one step towards its drop and reports whether
// it dropped it.
func (r *Repository) retireMainTable(ctx context.Context, f Family, table string, now time.Time) (bool, error) {
	dropped := false
	err := r.execLow(f, func(db *sql.DB) error {
		if has, err := hasTable(ctx, db, table); err != nil || !has {
			return err
		}
		var retiredAt time.Time
		err := db.QueryRowContext(ctx, `SELECT retired_at FROM retired_tables WHERE name = ?`, table).Scan(&retiredAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if empty, err := tableEmpty(ctx, db, table); err != nil || !empty {
				return err
			}
			_, err = db.ExecContext(ctx, `INSERT INTO retired_tables (name, retired_at) VALUES (?, ?)`, table, now.UTC())
			return err
		case err != nil:
			return err
		case now.Sub(retiredAt) < shardDeleteDelay:
			return nil
		}
		dropped, err = dropRetiredTable(ctx, db, table)
		return err
	})
	return dropped, err
}

// dropRetiredTable drops a retired table and forgets it, in one transaction.
// A table that has rows again (shards were off for a while) is only
// forgotten, so readers include it again.
func dropRetiredTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var rows bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+`)`).Scan(&rows); err != nil {
		return false, err
	}
	if !rows {
		if _, err := tx.ExecContext(ctx, `DROP TABLE `+table); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM retired_tables WHERE name = ?`, table); err != nil {
		return false, err
	}
	return !rows, tx.Commit()
}

func tableEmpty(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var rows bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+`)`).Scan(&rows)
	return !rows, err
}

// mainHolds reports whether main holds table for reads: it has the table and
// has not listed it as retiring. A main that predates retired_tables holds
// every table it has.
func mainHolds(ctx context.Context, db *sql.DB, table string) (bool, error) {
	has, err := hasTable(ctx, db, table)
	if err != nil || !has {
		return false, err
	}
	tracked, err := hasTable(ctx, db, "retired_tables")
	if err != nil {
		return false, err
	}
	if !tracked {
		return true, nil
	}
	var retired bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM retired_tables WHERE name = ?)`, table).Scan(&retired)
	return !retired, err
}

// restoreMainTables recreates the tables of every heavy family that is not
// sharded and that an earlier run with shards dropped from main, so a writer
// with SPANBARN_SHARDS off inserts into main again. The shard files keep the
// rows written while shards were on.
func restoreMainTables(ctx context.Context, db *sql.DB, sharded ShardRetention) error {
	for _, f := range Families() {
		if _, ok := sharded[f]; ok || !heavyFamily(f) {
			continue
		}
		has, err := hasTable(ctx, db, f.Tables()[0])
		if err != nil {
			return err
		}
		if has {
			continue
		}
		ddl, err := cachedBaselineDDL(ctx, f)
		if err != nil {
			return err
		}
		for _, stmt := range ddl {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("restore %s tables: %w", f, err)
			}
		}
	}
	return nil
}
