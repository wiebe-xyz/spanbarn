package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// ShardsDir returns the directory that holds the time-shard files of the main
// database at dbPath.
func ShardsDir(dbPath string) string { return dbPath + ".d" }

// shardLead is how long before a period starts the manager creates its file,
// so the first write of a period does not wait for a migration.
const shardLead = time.Hour

// ShardRetention is the retention window of each sharded family. A family
// with a zero window keeps writing to main.
type ShardRetention map[Family]time.Duration

// shardedFamilies lists the families that can be time-sharded, and whether
// their period counts in ISO weeks.
var shardedFamilies = []struct {
	family Family
	weekly bool
}{
	{FamilyLogs, false},
	{FamilyMetrics, false},
	{FamilyPrompts, true},
}

type shardKey struct {
	family Family
	start  time.Time
}

// ShardManager owns the time-shard files of the heavy families: it creates
// them ahead of each period, records them in main's shards table, and gives
// each write the handle of the shard its row belongs to. Only the writer runs
// one.
type ShardManager struct {
	dir    string
	main   *sql.DB
	open   OpenOptions
	specs  map[Family]shardSpec
	logger *slog.Logger
	now    func() time.Time

	mu         sync.Mutex
	handles    map[shardKey]*DB
	schedulers [numFamilies]*writescheduler.Scheduler

	// closing counts the goroutines closing ended shards, and closeMu runs
	// those closes one at a time.
	closing sync.WaitGroup
	closeMu sync.Mutex
	// beforeClose, when set, runs ahead of each ended shard's close. Tests
	// use it to hold a close open.
	beforeClose func()
}

// openShardManager prepares the shards directory next to dbPath, brings the
// shards table in line with the files on disk, migrates every listed shard,
// and creates the current period's file of each family.
func openShardManager(ctx context.Context, main *sql.DB, dbPath string, retention ShardRetention, o StorageOptions) (*ShardManager, error) {
	m := &ShardManager{
		dir:     ShardsDir(dbPath),
		main:    main,
		open:    OpenOptions{CacheMB: o.AttachCacheMB, MmapMB: o.AttachMmapMB},
		specs:   map[Family]shardSpec{},
		logger:  o.logger(),
		now:     time.Now,
		handles: map[shardKey]*DB{},
	}
	if o.Now != nil {
		m.now = o.Now
	}
	for _, sf := range shardedFamilies {
		if r := retention[sf.family]; r > 0 {
			m.specs[sf.family] = newShardSpec(sf.family, sf.weekly, r)
		}
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("shards directory: %w", err)
	}
	if err := m.reconcile(ctx); err != nil {
		return nil, err
	}
	if err := m.ensure(ctx, m.now()); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Families returns the families this manager shards, in family order.
func (m *ShardManager) Families() []Family {
	out := make([]Family, 0, len(m.specs))
	for f := range m.specs {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Sharded reports whether f's writes go to shard files.
func (m *ShardManager) Sharded(f Family) bool {
	if m == nil {
		return false
	}
	_, ok := m.specs[f]
	return ok
}

// SetScheduler names the write queue of f's shard inserts. A close of an
// ended shard goes through it too, so it never runs while a write holds the
// handle.
func (m *ShardManager) SetScheduler(f Family, s *writescheduler.Scheduler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedulers[f] = s
}

// submit runs fn on f's queue against the shard that holds rows ingested at
// the time fn runs. The shard is picked inside the queued write, so a write
// queued before midnight and run after it lands in the new day.
func (m *ShardManager) submit(f Family, label string, fn func(db *sql.DB) (int64, error)) error {
	return m.onQueueRows(f, label, func() (int64, error) {
		spec := m.specs[f]
		h, err := m.shard(context.Background(), spec, spec.start(m.now()))
		if err != nil {
			return 0, err
		}
		return fn(h.DB)
	})
}

// shard returns the open handle of the period of spec that starts at start,
// creating and recording the file if needed.
func (m *ShardManager) shard(ctx context.Context, spec shardSpec, start time.Time) (*DB, error) {
	key := shardKey{spec.family, start}
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.handles[key]; ok {
		return h, nil
	}
	file := spec.file(start)
	h, err := m.openShard(ctx, spec.family, start, file)
	if err != nil {
		return nil, err
	}
	// The file exists and is migrated before its row: a stop in between
	// leaves a file without a row, which reconcile records on the next start.
	if err := m.record(ctx, spec, start, file); err != nil {
		h.Close()
		return nil, err
	}
	m.handles[key] = h
	return h, nil
}

// record adds the row of a shard file to the shards table.
func (m *ShardManager) record(ctx context.Context, spec shardSpec, start time.Time, file string) error {
	if _, err := m.main.ExecContext(ctx, `INSERT OR IGNORE INTO shards
		(family, period_start, period_end, file) VALUES (?, ?, ?, ?)`,
		spec.family.String(), start.Format(time.DateOnly), start.Add(spec.length()).Format(time.DateOnly), file); err != nil {
		return fmt.Errorf("record shard %s: %w", file, err)
	}
	return nil
}

// openShard opens and migrates the shard file named file, whose period starts
// at start, and seeds its id sequences.
func (m *ShardManager) openShard(ctx context.Context, f Family, start time.Time, file string) (*DB, error) {
	h, err := Open(filepath.Join(m.dir, file), m.open)
	if err != nil {
		return nil, err
	}
	// Row deletes inside a shard (the disk ladder's trim) return their pages
	// to the volume only with incremental vacuum, which a file takes while it
	// is empty.
	if err := enableIncrementalVacuum(ctx, h.DB); err != nil {
		h.Close()
		return nil, fmt.Errorf("shard %s auto_vacuum: %w", file, err)
	}
	if err := MigrateShard(ctx, h.DB, f); err != nil {
		h.Close()
		return nil, fmt.Errorf("migrate shard %s: %w", file, err)
	}
	if err := seedShardIDs(ctx, h.DB, f, start); err != nil {
		h.Close()
		return nil, fmt.Errorf("seed shard %s: %w", file, err)
	}
	return h, nil
}

// seedShardIDs starts the AUTOINCREMENT ids of a new shard at
// shardIDBase(start). Every shard would otherwise count from 1, and the read
// views union the shards, so two rows would share an id. A sequence that
// already has a row (the shard holds or held rows) is left alone.
func seedShardIDs(ctx context.Context, db *sql.DB, f Family, start time.Time) error {
	for _, table := range f.Tables() {
		if _, err := db.ExecContext(ctx, `INSERT INTO sqlite_sequence (name, seq)
			SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = ?)`,
			table, shardIDBase(start), table); err != nil {
			return err
		}
	}
	return nil
}

// shardIDBase is the first id of the shard whose period starts at start: the
// day number since the Unix epoch, shifted left 32 bits. A shard holds up to
// 2^32 rows before it reaches the next day's base, main's ids stay below the
// first base, and every id stays under 2^53, so JSON clients read it exactly.
func shardIDBase(start time.Time) int64 {
	return int64(start.Sub(time.Unix(0, 0))/day) << 32
}

// Maintain creates the shards of the current period and of the period that
// starts within shardLead, closes the handles of periods that have ended, and
// checkpoints the open ones. The writer calls it on a ticker.
func (m *ShardManager) Maintain(ctx context.Context) error {
	now := m.now()
	if err := m.ensure(ctx, now); err != nil {
		return err
	}
	if err := m.ensure(ctx, now.Add(shardLead)); err != nil {
		return err
	}
	m.closeEnded(now)
	m.mu.Lock()
	open := make([]*DB, 0, len(m.handles))
	for _, h := range m.handles {
		open = append(open, h)
	}
	m.mu.Unlock()
	for _, h := range open {
		h.periodicCheckpoint(ctx, 0, m.logger)
	}
	return nil
}

// Run calls Maintain every interval until ctx is cancelled.
func (m *ShardManager) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Maintain(ctx); err != nil && ctx.Err() == nil {
				m.logger.Error("shard maintenance failed", "error", err)
			}
		}
	}
}

// ensure creates the shard of every family for the period that holds t.
func (m *ShardManager) ensure(ctx context.Context, t time.Time) error {
	for _, f := range m.Families() {
		spec := m.specs[f]
		if _, err := m.shard(ctx, spec, spec.start(t)); err != nil {
			return err
		}
	}
	return nil
}

// closeEnded closes the handles of periods that ended before now. Writes pick
// their shard by the time they run, so no write reaches an ended period once
// its handle is out of the map. A write that picked the handle before then
// still runs on it, so a no-op barrier goes through the family's queue first;
// once it has run, the handle is idle and the checkpoint and close run outside
// the write slot, one shard at a time.
//
// They ran inside the slot until 2026-10-28. At the day rollover the close
// held the slot 36 to 45s, mostly in fsync, and every write of the family
// waited behind it (#379).
func (m *ShardManager) closeEnded(now time.Time) {
	m.mu.Lock()
	var ended, inline []*DB
	var queues []*writescheduler.Scheduler
	for key, h := range m.handles {
		if now.Before(key.start.Add(m.specs[key.family].length())) {
			continue
		}
		delete(m.handles, key)
		// A family with no queue has no write slot to keep free.
		if q := m.schedulers[key.family]; q != nil {
			ended = append(ended, h)
			queues = append(queues, q)
		} else {
			inline = append(inline, h)
		}
	}
	if len(ended) > 0 {
		m.closing.Add(1)
		go m.closeAfterQueues(ended, queues)
	}
	m.mu.Unlock()
	for _, h := range inline {
		m.closeShard(h)
	}
}

// closeAfterQueues closes each of ended once the writes queued on its family's
// queue have run.
func (m *ShardManager) closeAfterQueues(ended []*DB, queues []*writescheduler.Scheduler) {
	defer m.closing.Done()
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	for i, h := range ended {
		m.awaitIdle(queues[i])
		if m.beforeClose != nil {
			m.beforeClose()
		}
		m.closeShard(h)
	}
}

// awaitIdle returns once every write queued on q before the call has run. A
// stopped queue has run all its writes, so its ErrStopped is a pass too.
func (m *ShardManager) awaitIdle(q *writescheduler.Scheduler) {
	if q == nil {
		return
	}
	err := q.Submit(context.Background(), writescheduler.Low, "closeShardBarrier", func() error { return nil })
	if err != nil && !errors.Is(err, writescheduler.ErrStopped) {
		m.logger.Error("close ended shard: queue barrier", "error", err)
	}
}

// closeShard checkpoints and closes an ended shard's handle. The close no
// longer reports through a write job, so its errors log at error level here.
func (m *ShardManager) closeShard(h *DB) {
	file := filepath.Base(h.path)
	if frames := h.FinalCheckpoint(m.logger); frames < 0 {
		m.logger.Error("close ended shard: final checkpoint failed", "file", file)
	}
	if err := h.Close(); err != nil {
		m.logger.Error("close ended shard", "file", file, "error", err)
	}
}

// Close waits for the closes of ended shards still running, then checkpoints
// and closes every open shard. Call after the writes have stopped.
func (m *ShardManager) Close() error {
	m.closing.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []error
	for key, h := range m.handles {
		h.FinalCheckpoint(m.logger)
		errs = append(errs, h.Close())
		delete(m.handles, key)
	}
	return errors.Join(errs...)
}
