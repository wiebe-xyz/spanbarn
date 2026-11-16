package engine

import (
	"context"
	"database/sql"
	"fmt"
)

// HeavyFamily reports whether f is a family whose tables main stops holding
// once shards take its inserts.
func HeavyFamily(f Family) bool {
	return f == FamilyLogs || f == FamilyMetrics || f == FamilyPrompts
}

// DropRetiredTable drops a retired table and forgets it, in one transaction.
// A table that has rows again (shards were off for a while) is only
// forgotten, so readers include it again.
func DropRetiredTable(ctx context.Context, db *sql.DB, table string) (bool, error) {
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

func TableEmpty(ctx context.Context, db *sql.DB, table string) (bool, error) {
	var rows bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+`)`).Scan(&rows)
	return !rows, err
}

// mainHolds reports whether main holds table for reads: it has the table and
// has not listed it as retiring. A main that predates retired_tables holds
// every table it has.
func mainHolds(ctx context.Context, db *sql.DB, table string) (bool, error) {
	has, err := HasTable(ctx, db, table)
	if err != nil || !has {
		return false, err
	}
	tracked, err := HasTable(ctx, db, "retired_tables")
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
		if _, ok := sharded[f]; ok || !HeavyFamily(f) {
			continue
		}
		has, err := HasTable(ctx, db, f.Tables()[0])
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
