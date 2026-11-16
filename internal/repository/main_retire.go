package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// retireMainTables drops the heavy tables main still holds from before the
// shards, once row retention has emptied them. A table goes in two steps,
// like a shard file: the writer lists it in retired_tables, readers leave it
// out of the family view on their next refresh, and after engine.ShardDeleteDelay
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
				r.shards.Logger().Info("main table dropped", "table", table)
			}
		}
	}
	return dropped, nil
}

// retireMainTable takes table one step towards its drop and reports whether
// it dropped it.
func (r *Repository) retireMainTable(ctx context.Context, f engine.Family, table string, now time.Time) (bool, error) {
	dropped := false
	err := r.execLow(f, func(db *sql.DB) error {
		if has, err := engine.HasTable(ctx, db, table); err != nil || !has {
			return err
		}
		var retiredAt time.Time
		err := db.QueryRowContext(ctx, `SELECT retired_at FROM retired_tables WHERE name = ?`, table).Scan(&retiredAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if empty, err := engine.TableEmpty(ctx, db, table); err != nil || !empty {
				return err
			}
			_, err = db.ExecContext(ctx, `INSERT INTO retired_tables (name, retired_at) VALUES (?, ?)`, table, now.UTC())
			return err
		case err != nil:
			return err
		case now.Sub(retiredAt) < engine.ShardDeleteDelay:
			return nil
		}
		dropped, err = engine.DropRetiredTable(ctx, db, table)
		return err
	})
	return dropped, err
}
