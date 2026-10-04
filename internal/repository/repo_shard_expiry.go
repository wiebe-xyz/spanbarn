package repository

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"
)

// ShardCutoffs are the retention cutoffs of the sharded families. ErrorLogs
// is the cutoff of the error-trace logs that kept_logs keeps.
type ShardCutoffs struct {
	Logs      time.Time
	Metrics   time.Time
	Prompts   time.Time
	ErrorLogs time.Time
}

func (c ShardCutoffs) of(f Family) time.Time {
	switch f {
	case FamilyLogs:
		return c.Logs
	case FamilyMetrics:
		return c.Metrics
	case FamilyPrompts:
		return c.Prompts
	}
	return time.Time{}
}

// ShardExpiry counts what one ExpireShards call did.
type ShardExpiry struct {
	Retired     int
	Deleted     int
	RowsTrimmed int64
	// MainTablesDropped counts the heavy tables main dropped after row
	// retention emptied them.
	MainTablesDropped int
}

// ExpireShards applies retention to the shard files. A shard whose whole
// period is older than its family's cutoff is marked retiring, after its
// error-trace and pinned logs are copied to kept_logs; a shard retiring for
// shardDeleteDelay is deleted. A window shorter than one period (the disk
// ladder cut it) also deletes the rows older than the cutoff inside the shard
// that straddles it. Main's copy of a sharded family's table is dropped once
// it is empty. Without shards it does nothing.
func (r *Repository) ExpireShards(ctx context.Context, now time.Time, c ShardCutoffs) (ShardExpiry, error) {
	var out ShardExpiry
	m := r.shards
	if m == nil {
		return out, nil
	}
	rows, err := m.rows(ctx)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := r.expireShard(ctx, row, now, c, &out); err != nil {
			return out, err
		}
	}
	out.MainTablesDropped, err = r.retireMainTables(ctx, now)
	return out, err
}

// expireShard applies retention to one shard and counts what it did in out.
func (r *Repository) expireShard(ctx context.Context, row shardRow, now time.Time, c ShardCutoffs, out *ShardExpiry) error {
	m := r.shards
	cutoff := c.of(row.family)
	switch {
	case row.state == shardRetiring:
		if now.Sub(row.retiringAt) < shardDeleteDelay {
			return nil
		}
		if err := m.remove(ctx, row); err != nil {
			return err
		}
		out.Deleted++
	case cutoff.IsZero():
	case !row.end.After(cutoff) && !m.writable(row, now):
		if err := r.retireShard(ctx, row, now, c.ErrorLogs); err != nil {
			return err
		}
		out.Retired++
	case now.Sub(cutoff) < m.specs[row.family].length() && row.start.Before(cutoff) && cutoff.Before(row.end):
		n, err := r.trimShard(ctx, row, cutoff, c.ErrorLogs)
		out.RowsTrimmed += n
		return err
	}
	return nil
}

// RetireOldestShard retires the oldest shard of any family that inserts no
// longer reach, and returns the bytes its deletion will free. The disk
// ladder's reclaim calls it before it evicts span rows. It reports false when
// no shard can go.
func (r *Repository) RetireOldestShard(ctx context.Context, now, errorLogCutoff time.Time) (int64, bool, error) {
	m := r.shards
	if m == nil {
		return 0, false, nil
	}
	rows, err := m.rows(ctx)
	if err != nil {
		return 0, false, err
	}
	for _, row := range rows {
		if row.state != shardActive || m.writable(row, now) {
			continue
		}
		if err := r.retireShard(ctx, row, now, errorLogCutoff); err != nil {
			return 0, false, err
		}
		db, wal := m.fileBytes(row.file)
		return db + wal, true, nil
	}
	return 0, false, nil
}

// RetiringShardBytes returns the bytes of the shards waiting for deletion:
// space the volume gets back within shardDeleteDelay.
func (r *Repository) RetiringShardBytes(ctx context.Context) (int64, error) {
	m := r.shards
	if m == nil {
		return 0, nil
	}
	rows, err := m.rows(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, row := range rows {
		if row.state == shardRetiring {
			db, wal := m.fileBytes(row.file)
			total += db + wal
		}
	}
	return total, nil
}

func (r *Repository) retireShard(ctx context.Context, row shardRow, now, errorLogCutoff time.Time) error {
	if row.family == FamilyLogs {
		if err := r.keepLogs(ctx, row, row.end, errorLogCutoff); err != nil {
			return err
		}
	}
	return r.shards.markRetiring(ctx, row, now)
}

// keepLogs copies the logs of error-sampled and pinned traces ingested before
// before from a logs shard into kept_logs. It runs on main's queue, where
// pinned_traces lives and error_samples is attached (or in main, in the
// single-file layout). kept_logs keeps the source ids, so a repeated copy
// inserts nothing.
func (r *Repository) keepLogs(ctx context.Context, row shardRow, before, errorLogCutoff time.Time) error {
	path := filepath.Join(r.shards.dir, row.file)
	return r.execLow(FamilyCore, func(db *sql.DB) error {
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS expiring`, "file:"+path+"?mode=ro"); err != nil {
			return fmt.Errorf("attach %s: %w", row.file, err)
		}
		defer func() { _, _ = conn.ExecContext(context.Background(), `DETACH DATABASE expiring`) }()
		_, err = conn.ExecContext(ctx, `INSERT OR IGNORE INTO main.kept_logs
			SELECT * FROM expiring.logs l
			WHERE l.ingested_at < ? AND l.trace_id IS NOT NULL
			  AND (EXISTS (SELECT 1 FROM pinned_traces p
			               WHERE p.project_id = l.project_id AND p.trace_id = l.trace_id)
			       OR EXISTS (SELECT 1 FROM error_samples e
			                  WHERE e.trace_id = l.trace_id AND e.sampled_at > ?))`,
			before.UTC(), errorLogCutoff.UTC())
		if err != nil {
			return fmt.Errorf("keep logs of %s: %w", row.file, err)
		}
		return nil
	})
}

// trimShard deletes the rows ingested before cutoff from one shard, per
// project and in batches along its (project_id, ingested_at) index, then
// returns the freed pages to the volume.
func (r *Repository) trimShard(ctx context.Context, row shardRow, cutoff, errorLogCutoff time.Time) (int64, error) {
	if row.family == FamilyLogs {
		if err := r.keepLogs(ctx, row, cutoff, errorLogCutoff); err != nil {
			return 0, err
		}
	}
	table := row.family.Tables()[0]
	m := r.shards
	var pids []int64
	if err := m.onShard(row.family, row.start, "trimShard", func(db *sql.DB) error {
		var err error
		pids, err = projectIDs(ctx, db, table)
		return err
	}); err != nil {
		return 0, err
	}
	q := `DELETE FROM ` + table + ` WHERE rowid IN (SELECT rowid FROM ` + table +
		` WHERE project_id = ? AND ingested_at < ? LIMIT ?)`
	var total int64
	for _, pid := range pids {
		for {
			var n int64
			if err := m.onShard(row.family, row.start, "trimShard", func(db *sql.DB) error {
				res, err := db.ExecContext(ctx, q, pid, cutoff.UTC(), retentionDeleteBatch)
				if err != nil {
					return err
				}
				n, _ = res.RowsAffected()
				return nil
			}); err != nil {
				return total, err
			}
			total += n
			if n < retentionDeleteBatch || ctx.Err() != nil {
				break
			}
		}
	}
	err := m.onShard(row.family, row.start, "trimShard", func(db *sql.DB) error {
		_, err := db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
		return err
	})
	return total, err
}

func projectIDs(ctx context.Context, db *sql.DB, table string) ([]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT project_id FROM `+table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
