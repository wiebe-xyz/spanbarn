package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection to SQLite.
type DB struct {
	*sql.DB
}

func sqliteSpanName(_ context.Context, _ otelsql.Method, query string) string {
	if query != "" {
		if i := strings.IndexByte(query, ' '); i > 0 {
			return "sqlite." + strings.ToLower(strings.TrimSpace(query[:i]))
		}
	}
	return "sqlite.exec"
}

var otelOpts = []otelsql.Option{
	otelsql.WithAttributes(semconv.DBSystemSqlite),
	otelsql.WithSpanNameFormatter(sqliteSpanName),
}

// Default page cache / mmap sizes, in MiB, for the writer and read-only
// connections. These were tuned against production's ~900 MB DB under a 2Gi
// writer pod (PR #113) and are the values NewDB/NewReadOnlyDB use unless a
// caller opts into different sizing via NewDBWithCache/NewReadOnlyDBWithCache
// — smaller, memory-constrained pods (e.g. testing/staging) should pass
// smaller values rather than inherit these.
const (
	DefaultWriterCacheMB   = 256
	DefaultWriterMmapMB    = 2048
	DefaultReadOnlyCacheMB = 32
	DefaultReadOnlyMmapMB  = 1024
)

// Pragmas are applied via DSN query params (_pragma=...) so they take effect on
// every connection the pool opens — not just the first one. Setting them via
// db.Exec("PRAGMA …") only configures the single connection that handled the
// Exec call; later connections spawned under load inherit no pragmas, so
// busy_timeout defaults to 0 and any contention surfaces as SQLITE_BUSY.
func buildDSN(dbPath string, readOnly bool, cacheMB, mmapMB int) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
		// Disable SQLite's automatic passive checkpoints; the writer issues
		// explicit TRUNCATE checkpoints on a fixed interval instead (see
		// RunPeriodicCheckpoint). SQLite's own automatic passive checkpoints
		// silently stop at any reader snapshot boundary, so they cannot prevent
		// unbounded WAL growth under sustained read load.
		q.Add("_pragma", "wal_autocheckpoint(0)")
	}
	if readOnly {
		q.Set("mode", "ro")
	}
	// Keep the index working set resident. The spans table carries ~12
	// indexes, so every insert traverses many B-trees; with the SQLite
	// default 2 MiB cache against a large DB those pages were evicted and
	// pread back from disk on every batch, collapsing write throughput below
	// the ingest rate and backing up the redis write-queue. The page cache
	// holds the hot interior pages; the mmap window serves the rest from the
	// OS page cache (reclaimable) instead of per-page pread. Caller sizes
	// both to fit under the pod's GOMEMLIMIT / memory limit.
	q.Add("_pragma", fmt.Sprintf("cache_size(-%d)", cacheMB*1024)) // negative = KiB
	q.Add("_pragma", fmt.Sprintf("mmap_size(%d)", mmapMB*1024*1024))
	prefix := "file:"
	if !strings.HasPrefix(dbPath, "file:") && dbPath != ":memory:" {
		dbPath = prefix + dbPath
	}
	return dbPath + "?" + q.Encode()
}

// NewDB opens a SQLite database at dbPath with WAL mode, busy timeout, foreign
// keys enabled, and the default writer cache/mmap sizing (see
// DefaultWriterCacheMB/DefaultWriterMmapMB). Use NewDBWithCache to size a
// memory-constrained pod's page cache instead.
func NewDB(dbPath string) (*DB, error) {
	return NewDBWithCache(dbPath, DefaultWriterCacheMB, DefaultWriterMmapMB)
}

// NewDBWithCache is NewDB with an explicit page cache / mmap window size (MiB).
// MaxOpenConns is capped at 1: SQLite allows only one writer at a time, and when two goroutines
// each hold a separate connection and try to upgrade a deferred transaction to a write lock they
// deadlock each other — SQLite returns SQLITE_BUSY immediately, before busy_timeout can help.
// Serialising through one connection means the second writer waits in Go's pool (not in SQLite).
func NewDBWithCache(dbPath string, cacheMB, mmapMB int) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, false, cacheMB, mmapMB), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	// Expose connection pool stats as OTel metrics (best-effort).
	_, _ = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(semconv.DBSystemSqlite))
	return &DB{DB: db}, nil
}

// NewReadOnlyDB opens an existing SQLite database at dbPath in read-only mode
// with the default read-only cache/mmap sizing (see
// DefaultReadOnlyCacheMB/DefaultReadOnlyMmapMB). Use NewReadOnlyDBWithCache to
// size a memory-constrained pod's page cache instead. Safe to use concurrently
// with a writer process on the same file when WAL mode is active on that file.
func NewReadOnlyDB(dbPath string) (*DB, error) {
	return NewReadOnlyDBWithCache(dbPath, DefaultReadOnlyCacheMB, DefaultReadOnlyMmapMB)
}

// NewReadOnlyDBWithCache is NewReadOnlyDB with an explicit page cache / mmap
// window size (MiB).
func NewReadOnlyDBWithCache(dbPath string, cacheMB, mmapMB int) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, true, cacheMB, mmapMB), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	return &DB{DB: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}

// RunPeriodicCheckpoint blocks until ctx is cancelled, issuing a TRUNCATE WAL
// checkpoint on each tick. When a checkpoint returns busy=1 (a reader snapshot
// blocks full WAL backfill) it retries every retryInterval until the readers
// release or the next full-interval tick arrives.
//
// Note: a TRUNCATE checkpoint does NOT automatically retry via busy_timeout
// when readers block WAL backfill — it returns SQLITE_BUSY immediately in
// that phase. busy_timeout only applies to write-lock conflicts between
// concurrent writers. Application-level retry (done here) is required to
// eventually drain the WAL under read load.
//
// Call on the same writer *DB as every other writer. An earlier version used a
// dedicated second connection to avoid queueing behind worker transactions, but
// SQLite still only allows one writer at a time at the file level — a second
// connection just moves the contention from Go's pool into SQLite's lock
// machinery, where a deferred-tx upgrade against this connection's RESERVED
// lock returns SQLITE_BUSY immediately and bypasses busy_timeout (root cause of
// SPA-27). In-process serialisation via MaxOpenConns(1) is cheap because the
// write transactions are tiny (small aggregate upserts, short CRUD txs).
//
// The checkpoint runs unconditionally on every tick. An earlier version skipped
// checkpoints while the Redis write-queue backlog was deep, on the theory that a
// checkpoint competes with backlog-draining writes on the single connection. In
// production the stale backlog kept that gate tripped permanently, so no
// checkpoint ever ran and the WAL bloated to hundreds of MB — which made *every*
// write slow (each op walks the WAL), the exact opposite of the intended effect,
// and even stalled a schema migration on first open. Running it every tick
// keeps the WAL small and writes fast, so there is no gate.
func (d *DB) RunPeriodicCheckpoint(ctx context.Context, interval time.Duration, log *slog.Logger) {
	retryInterval := 5 * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.checkpoint(ctx, retryInterval, log)
		}
	}
}

// FinalCheckpoint runs one WAL checkpoint on a fresh context. Call after all
// writers have stopped (e.g. after wg.Wait()) and before db.Close(), so the
// WAL is merged into the main file on every clean shutdown. With
// wal_autocheckpoint(0), db.Close() does not checkpoint automatically.
func (d *DB) FinalCheckpoint(log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.checkpoint(ctx, 0, log)
}

// checkpoint runs one wal_checkpoint(TRUNCATE) and returns the WAL size in
// frames after the attempt (the pragma's `log` column), or -1 if it errored.
// It retries on busy=1 (reader snapshot blocking backfill) so the WAL is
// actually reset once the reader releases.
func (d *DB) checkpoint(ctx context.Context, retryInterval time.Duration, log *slog.Logger) int {
	for {
		var busy, walFrames, checkpointed int
		if err := d.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
			if ctx.Err() == nil {
				log.Warn("wal checkpoint error", "error", err)
			}
			return -1
		}
		if busy == 0 {
			// Reclaim up to 5000 freed pages (~20 MiB) per tick. No-op when
			// auto_vacuum != INCREMENTAL (i.e. before migration 018 has run).
			_, _ = d.ExecContext(ctx, "PRAGMA incremental_vacuum(5000)")
			return walFrames
		}
		log.Debug("wal checkpoint blocked by reader, retrying", "wal_frames", walFrames, "checkpointed", checkpointed)
		if retryInterval == 0 {
			return walFrames
		}
		select {
		case <-ctx.Done():
			return walFrames
		case <-time.After(retryInterval):
		}
	}
}
