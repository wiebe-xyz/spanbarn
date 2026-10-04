package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// maxAttached is the most files a connection of the modernc build can attach.
const maxAttached = 10

// poolCloseDelay is how long a replaced family pool stays open, so a query
// that picked it just before the swap still runs.
const poolCloseDelay = time.Minute

// familyPool is a read pool over one sharded family. Each connection attaches
// the family's shards and creates a TEMP view per table that unions them with
// main's table, so queries keep their unqualified table names: SQLite resolves
// a name in temp before main.
type familyPool struct {
	db *DB
	// files are the attached shard files, newest period first.
	files []string
	// mainHas reports whether main still holds the family's tables (rows
	// written before the shards).
	mainHas bool
	// kept reports whether the logs view includes main's kept_logs: the
	// error-trace and pinned logs copied out of expired logs shards.
	kept bool
	// segments are the schemas that hold the family's rows, newest first:
	// one per shard, then main when it holds the tables.
	segments []string
}

func (p *familyPool) same(files []string, mainHas, kept bool) bool {
	return p != nil && p.mainHas == mainHas && p.kept == kept && slices.Equal(p.files, files)
}

// ShardReaders keeps a read pool per sharded family. Refresh rebuilds a
// family's pool when its active shards change; the writer marks a shard
// retiring before it deletes it, so a refresh drops the file from the pool
// first. A family without shards has no pool and reads through main.
type ShardReaders struct {
	main    *sql.DB
	dbPath  string
	cacheMB int
	mmapMB  int
	logger  *slog.Logger

	mu    sync.RWMutex
	pools [numFamilies]*familyPool
}

// NewShardReaders returns the shard readers of the database at dbPath. main is
// a read handle on that database, used to list the shards.
func NewShardReaders(main *sql.DB, dbPath string, cacheMB, mmapMB int, logger *slog.Logger) *ShardReaders {
	if logger == nil {
		logger = slog.Default()
	}
	return &ShardReaders{main: main, dbPath: dbPath, cacheMB: cacheMB, mmapMB: mmapMB, logger: logger}
}

// Run refreshes the pools every interval until ctx is cancelled.
func (s *ShardReaders) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("shard readers refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh reads the active shards from main and rebuilds the pool of every
// family whose shards changed.
func (s *ShardReaders) Refresh(ctx context.Context) error {
	files, err := s.activeShards(ctx)
	if err != nil {
		return err
	}
	for _, sf := range shardedFamilies {
		if err := s.refreshFamily(ctx, sf.family, files[sf.family]); err != nil {
			return err
		}
	}
	return nil
}

func (s *ShardReaders) refreshFamily(ctx context.Context, f Family, files []string) error {
	if len(files) > maxAttached {
		s.logger.Warn("more active shards than a connection can attach, reading the newest",
			"family", f.String(), "shards", len(files), "attached", maxAttached)
		files = files[:maxAttached]
	}
	mainHas, err := hasTable(ctx, s.main, f.Tables()[0])
	if err != nil {
		return err
	}
	kept := false
	if f == FamilyLogs {
		if kept, err = hasTable(ctx, s.main, "kept_logs"); err != nil {
			return err
		}
	}
	s.mu.RLock()
	current := s.pools[f]
	s.mu.RUnlock()
	if current.same(files, mainHas, kept) || (current == nil && len(files) == 0) {
		return nil
	}
	var next *familyPool
	if len(files) > 0 {
		if next, err = s.openPool(f, files, mainHas, kept); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.pools[f] = next
	s.mu.Unlock()
	if current != nil {
		time.AfterFunc(poolCloseDelay, func() { current.db.Close() })
	}
	s.logger.Info("shard read pool changed", "family", f.String(), "shards", len(files), "main", mainHas)
	return nil
}

// activeShards lists the active shard files per family, newest period first.
// A main without the shards table (a writer that has not migrated yet) has
// none.
func (s *ShardReaders) activeShards(ctx context.Context) (map[Family][]string, error) {
	out := map[Family][]string{}
	if has, err := hasTable(ctx, s.main, "shards"); err != nil || !has {
		return out, err
	}
	rows, err := s.main.QueryContext(ctx,
		`SELECT family, file FROM shards WHERE state = 'active' ORDER BY period_start DESC`)
	if err != nil {
		return nil, fmt.Errorf("list shards: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var family, file string
		if err := rows.Scan(&family, &file); err != nil {
			return nil, err
		}
		for _, sf := range shardedFamilies {
			if sf.family.String() == family {
				out[sf.family] = append(out[sf.family], file)
			}
		}
	}
	return out, rows.Err()
}

// openPool opens a read pool on main that attaches files, newest first, and
// views f's tables over them.
func (s *ShardReaders) openPool(f Family, files []string, mainHas, kept bool) (*familyPool, error) {
	p := &familyPool{files: files, mainHas: mainHas, kept: kept}
	attach := make([]Attachment, len(files))
	for i, file := range files {
		schema := fmt.Sprintf("shard_%d", i)
		attach[i] = Attachment{Schema: schema, Path: filepath.Join(ShardsDir(s.dbPath), file), CacheMB: s.cacheMB, MmapMB: s.mmapMB}
		p.segments = append(p.segments, schema)
	}
	if mainHas {
		p.segments = append(p.segments, "main")
	}
	views := make([]string, 0, len(f.Tables()))
	for _, table := range f.Tables() {
		parts := make([]string, len(p.segments))
		for i, schema := range p.segments {
			parts[i] = "SELECT * FROM " + schema + "." + table
		}
		if kept && table == "logs" {
			parts = append(parts, "SELECT * FROM main.kept_logs")
		}
		views = append(views, "CREATE TEMP VIEW "+table+" AS "+strings.Join(parts, " UNION ALL "))
	}
	db, err := Open(s.dbPath, OpenOptions{ReadOnly: true, CacheMB: s.cacheMB, MmapMB: s.mmapMB, Attach: attach, Setup: views})
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(readConnMaxLifetime)
	p.db = db
	return p, nil
}

// pool returns f's read pool, or nil when f has no shards.
func (s *ShardReaders) pool(f Family) *familyPool {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pools[f]
}

// Close closes every pool.
func (s *ShardReaders) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for f, p := range s.pools {
		if p != nil {
			p.db.Close()
			s.pools[f] = nil
		}
	}
}
