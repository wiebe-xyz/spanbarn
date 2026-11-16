package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// ShardDeleteDelay is how long a retiring shard's file stays on disk. Readers
// drop a retiring shard on their next refresh (30s), close the replaced pool a
// minute later, and recycle connections every two minutes, so after five
// minutes no reader holds the file open and deleting it frees its space.
const ShardDeleteDelay = 5 * time.Minute

const (
	ShardActive   = "active"
	ShardRetiring = "retiring"
)

// ShardRow is one row of the shards table.
type ShardRow struct {
	Family     Family
	File       string
	Start, End time.Time
	State      string
	RetiringAt time.Time
}

// Rows lists the shards of the families this manager shards, oldest period
// first.
func (m *ShardManager) Rows(ctx context.Context) ([]ShardRow, error) {
	rows, err := m.main.QueryContext(ctx, `SELECT family, file, period_start, period_end, state, retiring_at
		FROM shards ORDER BY period_start, family`)
	if err != nil {
		return nil, fmt.Errorf("read shards: %w", err)
	}
	defer rows.Close()
	var out []ShardRow
	for rows.Next() {
		var family, start, end string
		var retiring sql.NullTime
		var row ShardRow
		if err := rows.Scan(&family, &row.File, &start, &end, &row.State, &retiring); err != nil {
			return nil, err
		}
		f, ok := m.familyNamed(family)
		if !ok {
			continue
		}
		row.Family, row.RetiringAt = f, retiring.Time
		if row.Start, err = time.Parse(time.DateOnly, start); err != nil {
			return nil, fmt.Errorf("shard %s: %w", row.File, err)
		}
		if row.End, err = time.Parse(time.DateOnly, end); err != nil {
			return nil, fmt.Errorf("shard %s: %w", row.File, err)
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

// Writable reports whether row's period is the current one or a later one, so
// inserts may still reach it. Such a shard is never retired.
func (m *ShardManager) Writable(row ShardRow, now time.Time) bool {
	return !row.Start.Before(m.specs[row.Family].start(now))
}

// MarkRetiring sets an active shard retiring. Readers drop it on their next
// refresh.
func (m *ShardManager) MarkRetiring(ctx context.Context, row ShardRow, now time.Time) error {
	if _, err := m.main.ExecContext(ctx,
		`UPDATE shards SET state = ?, retiring_at = ? WHERE file = ? AND state = ?`,
		ShardRetiring, now.UTC(), row.File, ShardActive); err != nil {
		return fmt.Errorf("retire shard %s: %w", row.File, err)
	}
	m.logger.Info("shard retiring", "file", row.File)
	return nil
}

// Remove closes a retiring shard's handle, deletes its file and sidecars,
// then its row. A stop between the two leaves a row without a file, which
// reconcile removes on the next start.
func (m *ShardManager) Remove(ctx context.Context, row ShardRow) error {
	key := shardKey{row.Family, row.Start}
	m.mu.Lock()
	h := m.handles[key]
	delete(m.handles, key)
	m.mu.Unlock()
	if h != nil {
		if err := m.onQueue(row.Family, "closeShard", h.Close); err != nil {
			return err
		}
	}
	path := filepath.Join(m.dir, row.File)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete shard %s: %w", p, err)
		}
	}
	if _, err := m.main.ExecContext(ctx, `DELETE FROM shards WHERE file = ?`, row.File); err != nil {
		return fmt.Errorf("forget shard %s: %w", row.File, err)
	}
	m.logger.Info("shard deleted", "file", row.File)
	return nil
}

// OnShard runs fn on f's queue against the shard whose period starts at start.
func (m *ShardManager) OnShard(f Family, start time.Time, label string, fn func(db *sql.DB) error) error {
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
	return m.onQueueRows(f, label, func() (int64, error) { return writescheduler.NoRows, fn() })
}

// onQueueRows is onQueue for a write that reports how many rows it wrote, or
// writescheduler.NoRows.
func (m *ShardManager) onQueueRows(f Family, label string, fn func() (int64, error)) error {
	m.mu.Lock()
	s := m.schedulers[f]
	m.mu.Unlock()
	if s == nil {
		_, err := fn()
		return err
	}
	_, err := s.SubmitRows(context.Background(), writescheduler.Low, label, fn)
	return err
}

// FileBytes returns the size of a shard's file and its WAL.
func (m *ShardManager) FileBytes(file string) (db, wal int64) {
	path := filepath.Join(m.dir, file)
	if info, err := os.Stat(path); err == nil {
		db = info.Size()
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		wal = info.Size()
	}
	return db, wal
}

// AddSpace adds every shard file to s: file and WAL bytes, and the file's
// pages in s's page size. Shards return freed pages to the volume through
// incremental vacuum, so their freelist is left out.
func (m *ShardManager) AddSpace(ctx context.Context, s *Space) error {
	rows, err := m.Rows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		db, wal := m.FileBytes(row.File)
		s.FileBytes += db
		s.WALBytes += wal
		if s.PageSize > 0 {
			s.PageCount += db / s.PageSize
		}
	}
	return nil
}

// Dir is the directory that holds the shard files.
func (m *ShardManager) Dir() string { return m.dir }

// PeriodLength is how much time one shard file of f covers; zero when f is
// not sharded.
func (m *ShardManager) PeriodLength(f Family) time.Duration { return m.specs[f].length() }

// Logger is the logger the manager reports shard lifecycle events to.
func (m *ShardManager) Logger() *slog.Logger { return m.logger }
