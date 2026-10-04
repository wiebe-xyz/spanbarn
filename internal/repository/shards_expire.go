package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// shardDeleteDelay is how long a retiring shard's file stays on disk. Readers
// drop a retiring shard on their next refresh (30s), close the replaced pool a
// minute later, and recycle connections every two minutes, so after five
// minutes no reader holds the file open and deleting it frees its space.
const shardDeleteDelay = 5 * time.Minute

const (
	shardActive   = "active"
	shardRetiring = "retiring"
)

// shardRow is one row of the shards table.
type shardRow struct {
	family     Family
	file       string
	start, end time.Time
	state      string
	retiringAt time.Time
}

// rows lists the shards of the families this manager shards, oldest period
// first.
func (m *ShardManager) rows(ctx context.Context) ([]shardRow, error) {
	rows, err := m.main.QueryContext(ctx, `SELECT family, file, period_start, period_end, state, retiring_at
		FROM shards ORDER BY period_start, family`)
	if err != nil {
		return nil, fmt.Errorf("read shards: %w", err)
	}
	defer rows.Close()
	var out []shardRow
	for rows.Next() {
		var family, start, end string
		var retiring sql.NullTime
		var row shardRow
		if err := rows.Scan(&family, &row.file, &start, &end, &row.state, &retiring); err != nil {
			return nil, err
		}
		f, ok := m.familyNamed(family)
		if !ok {
			continue
		}
		row.family, row.retiringAt = f, retiring.Time
		if row.start, err = time.Parse(time.DateOnly, start); err != nil {
			return nil, fmt.Errorf("shard %s: %w", row.file, err)
		}
		if row.end, err = time.Parse(time.DateOnly, end); err != nil {
			return nil, fmt.Errorf("shard %s: %w", row.file, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (m *ShardManager) familyNamed(name string) (Family, bool) {
	for f := range m.specs {
		if f.String() == name {
			return f, true
		}
	}
	return 0, false
}

// writable reports whether row's period is the current one or a later one, so
// inserts may still reach it. Such a shard is never retired.
func (m *ShardManager) writable(row shardRow, now time.Time) bool {
	return !row.start.Before(m.specs[row.family].start(now))
}

// markRetiring sets an active shard retiring. Readers drop it on their next
// refresh.
func (m *ShardManager) markRetiring(ctx context.Context, row shardRow, now time.Time) error {
	if _, err := m.main.ExecContext(ctx,
		`UPDATE shards SET state = ?, retiring_at = ? WHERE file = ? AND state = ?`,
		shardRetiring, now.UTC(), row.file, shardActive); err != nil {
		return fmt.Errorf("retire shard %s: %w", row.file, err)
	}
	m.logger.Info("shard retiring", "file", row.file)
	return nil
}

// remove closes a retiring shard's handle, deletes its file and sidecars,
// then its row. A stop between the two leaves a row without a file, which
// reconcile removes on the next start.
func (m *ShardManager) remove(ctx context.Context, row shardRow) error {
	key := shardKey{row.family, row.start}
	m.mu.Lock()
	h := m.handles[key]
	delete(m.handles, key)
	m.mu.Unlock()
	if h != nil {
		if err := m.onQueue(row.family, "closeShard", h.Close); err != nil {
			return err
		}
	}
	path := filepath.Join(m.dir, row.file)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete shard %s: %w", p, err)
		}
	}
	if _, err := m.main.ExecContext(ctx, `DELETE FROM shards WHERE file = ?`, row.file); err != nil {
		return fmt.Errorf("forget shard %s: %w", row.file, err)
	}
	m.logger.Info("shard deleted", "file", row.file)
	return nil
}

// onShard runs fn on f's queue against the shard whose period starts at start.
func (m *ShardManager) onShard(f Family, start time.Time, label string, fn func(db *sql.DB) error) error {
	return m.onQueue(f, label, func() error {
		h, err := m.shard(context.Background(), m.specs[f], start)
		if err != nil {
			return err
		}
		return fn(h.DB)
	})
}

// onQueue runs fn on f's write queue, or inline without one.
func (m *ShardManager) onQueue(f Family, label string, fn func() error) error {
	m.mu.Lock()
	s := m.schedulers[f]
	m.mu.Unlock()
	if s == nil {
		return fn()
	}
	return s.Submit(context.Background(), writescheduler.Low, label, fn)
}

// fileBytes returns the size of a shard's file and its WAL.
func (m *ShardManager) fileBytes(file string) (db, wal int64) {
	path := filepath.Join(m.dir, file)
	if info, err := os.Stat(path); err == nil {
		db = info.Size()
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		wal = info.Size()
	}
	return db, wal
}

// addSpace adds every shard file to s: file and WAL bytes, and the file's
// pages in s's page size. Shards return freed pages to the volume through
// incremental vacuum, so their freelist is left out.
func (m *ShardManager) addSpace(ctx context.Context, s *Space) error {
	rows, err := m.rows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		db, wal := m.fileBytes(row.file)
		s.FileBytes += db
		s.WALBytes += wal
		if s.PageSize > 0 {
			s.PageCount += db / s.PageSize
		}
	}
	return nil
}
