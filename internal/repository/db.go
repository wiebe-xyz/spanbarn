package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/XSAM/otelsql"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB connection to SQLite.
type DB struct {
	*sql.DB
	setupKey string // connSetups key, "" when the handle attaches nothing

	// path is the file a writable handle opened, "" for a read-only or
	// in-memory handle. The periodic checkpoint opens its own connection to it.
	path     string
	ckptOnce sync.Once
	ckptDB   *sql.DB
	ckptErr  error
}

func sqliteSpanName(_ context.Context, _ otelsql.Method, query string) string {
	if query != "" {
		if i := strings.IndexByte(query, ' '); i > 0 {
			return "sqlite." + strings.ToLower(strings.TrimSpace(query[:i]))
		}
	}
	return "sqlite.exec"
}

// suppressTracingKey marks a context as telemetry-storage work.
type suppressTracingKey struct{}

// WithoutSpanTracing marks ctx so no otelsql span is created for queries run
// under it.
//
// Storing telemetry must not itself emit telemetry. Every span the writer
// inserts would otherwise emit its own sqlite.insert span, which must then be
// inserted, emitting another — a feedback loop whose volume grows with writer
// load. In production that recursion made sqlite.insert alone 876k spans (96%
// of SpanBarn's self-telemetry) and helped fill the disk.
//
// This is deliberately scoped to the telemetry write paths: reads and
// user-facing queries stay instrumented, since they cannot feed themselves.
func WithoutSpanTracing(ctx context.Context) context.Context {
	return context.WithValue(ctx, suppressTracingKey{}, true)
}

func spanTracingSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(suppressTracingKey{}).(bool)
	return v
}

var otelOpts = []otelsql.Option{
	otelsql.WithAttributes(semconv.DBSystemSqlite),
	otelsql.WithSpanNameFormatter(sqliteSpanName),
	otelsql.WithSpanOptions(otelsql.SpanOptions{
		SpanFilter: func(ctx context.Context, _ otelsql.Method, _ string, _ []driver.NamedValue) bool {
			return !spanTracingSuppressed(ctx)
		},
	}),
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
// busyTimeoutMs is how long a connection waits on a lock before SQLITE_BUSY.
const busyTimeoutMs = 30000

// walSizeLimitBytes is the size SQLite truncates a WAL file to when the writer
// restarts it (journal_size_limit).
const walSizeLimitBytes = 64 << 20

func buildDSN(dbPath string, readOnly bool, cacheMB, mmapMB int, setupKey string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs))
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
		// The periodic checkpoint is PASSIVE and leaves the WAL file at its
		// largest size; the writer reuses it from the start once a checkpoint
		// has backfilled it. When it does, SQLite truncates the file to this
		// limit, so one burst does not keep a large WAL on disk.
		q.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", walSizeLimitBytes))
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
	if setupKey != "" {
		q.Set(handleParam, setupKey)
	}
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
	return Open(dbPath, OpenOptions{CacheMB: cacheMB, MmapMB: mmapMB})
}

// OpenOptions configures one database handle.
type OpenOptions struct {
	ReadOnly bool
	CacheMB  int
	MmapMB   int
	// Attach lists files every connection attaches read-only, so the handle
	// sees their tables under their unqualified names (SQLite resolves a name
	// in temp, then main, then each attached database in attach order).
	Attach []Attachment
	// Setup lists statements every connection runs after its attachments.
	Setup []string
}

// Open opens dbPath with the given sizing and attachments. A writable handle
// is capped at one connection (see NewDBWithCache); a read-only one is not.
func Open(dbPath string, o OpenOptions) (*DB, error) {
	var key string
	if len(o.Attach) > 0 || len(o.Setup) > 0 {
		key = registerConnSetup(&ConnSetup{Attach: o.Attach, Statements: o.Setup})
	}
	mode := "sqlite"
	if o.ReadOnly {
		mode = "sqlite read-only"
	}
	driverName := "sqlite"
	if o.ReadOnly {
		driverName = recoverDriver()
	}
	db, err := otelsql.Open(driverName, buildDSN(dbPath, o.ReadOnly, o.CacheMB, o.MmapMB, key), otelOpts...)
	if err != nil {
		if key != "" {
			connSetups.Delete(key)
		}
		return nil, fmt.Errorf("open %s %s: %w", mode, dbPath, err)
	}
	h := &DB{DB: db, setupKey: key}
	if !o.ReadOnly {
		db.SetMaxOpenConns(1)
		// Expose connection pool stats as OTel metrics (best-effort).
		_, _ = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(semconv.DBSystemSqlite))
		if dbPath != ":memory:" && !strings.Contains(dbPath, "mode=memory") {
			h.path = dbPath
		}
	}
	return h, nil
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
	return Open(dbPath, OpenOptions{ReadOnly: true, CacheMB: cacheMB, MmapMB: mmapMB})
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	if d.setupKey != "" {
		connSetups.Delete(d.setupKey)
	}
	if d.ckptDB != nil {
		_ = d.ckptDB.Close()
	}
	return d.DB.Close()
}

// RunPeriodicCheckpoint blocks until ctx is cancelled, issuing a PASSIVE WAL
// checkpoint on each tick (see periodicCheckpoint).
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
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.periodicCheckpoint(ctx, log)
		}
	}
}

// periodicCheckpoint runs one PASSIVE checkpoint on a connection of its own and
// returns the WAL frames it could not backfill, or -1 if it errored.
//
// A checkpoint ends in fsync of the WAL and the database file. It used to run
// as TRUNCATE on the handle's single writer connection, so every write to the
// file waited for those fsyncs. On production's shared disk they take seconds
// and sometimes more than 30s: on 2026-10-23 a third of the writer's slow
// writes started within 3s of a checkpoint tick, and at 14:36Z the writers of
// four files waited 32s to 39s behind checkpoints in fsync (SPA-74, #360).
//
// PASSIVE takes no write lock, so it can run beside the writer. An earlier
// version ran TRUNCATE on a second connection, and TRUNCATE does take the
// write lock: a deferred write transaction on the writer then got SQLITE_BUSY
// at once (SPA-27). PASSIVE also never waits for readers; frames a reader
// still needs are left for the next tick. After a full backfill the writer
// starts the WAL over on its next write, and journal_size_limit bounds the
// file. A handle without a file (in-memory) checkpoints on its writer.
func (d *DB) periodicCheckpoint(ctx context.Context, log *slog.Logger) int {
	ckpt, err := d.checkpointer()
	if err != nil {
		log.Warn("wal checkpoint: open connection", "error", err)
		return -1
	}
	var busy, walFrames, checkpointed int
	if err := ckpt.QueryRowContext(ctx, "PRAGMA main.wal_checkpoint(PASSIVE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
		if ctx.Err() == nil {
			log.Warn("wal checkpoint error", "error", err)
		}
		return -1
	}
	left := walFrames - checkpointed
	if walFrames < 0 || checkpointed < 0 {
		// -1 means the file is not in WAL mode.
		left = 0
	}
	if left > 0 {
		log.Debug("wal checkpoint left frames a reader still uses", "wal_frames", walFrames, "checkpointed", checkpointed)
		return left
	}
	// Reclaim up to 5000 freed pages (~20 MiB) per tick. It is a write, so it
	// takes the writer; the check on the checkpoint connection keeps a file
	// with nothing to reclaim, or without auto_vacuum = INCREMENTAL (before
	// migration 018 has run), off the writer.
	if reclaimable(ctx, ckpt) {
		_, _ = d.ExecContext(ctx, "PRAGMA main.incremental_vacuum(5000)")
	}
	return 0
}

// reclaimable reports whether incremental_vacuum would free pages in db's main
// file: auto_vacuum is INCREMENTAL (2) and the freelist is not empty.
func reclaimable(ctx context.Context, db *sql.DB) bool {
	var mode, free int
	if err := db.QueryRowContext(ctx, "PRAGMA main.auto_vacuum").Scan(&mode); err != nil || mode != 2 {
		return false
	}
	if err := db.QueryRowContext(ctx, "PRAGMA main.freelist_count").Scan(&free); err != nil {
		return false
	}
	return free > 0
}

// checkpointer returns the connection the periodic checkpoint runs on: a pool
// of one connection to the handle's file with no attachments, opened on first
// use. A handle without a file returns its own pool.
func (d *DB) checkpointer() (*sql.DB, error) {
	if d.path == "" {
		return d.DB, nil
	}
	d.ckptOnce.Do(func() {
		db, err := sql.Open("sqlite", buildDSN(d.path, false, 1, 0, ""))
		if err != nil {
			d.ckptErr = err
			return
		}
		db.SetMaxOpenConns(1)
		d.ckptDB = db
	})
	return d.ckptDB, d.ckptErr
}

// FinalCheckpoint runs one WAL checkpoint on a fresh context. Call after all
// writers have stopped (e.g. after wg.Wait()) and before db.Close(), so the
// WAL is merged into the main file on every clean shutdown. With
// wal_autocheckpoint(0), db.Close() does not checkpoint automatically.
func (d *DB) FinalCheckpoint(log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.checkpoint(ctx, log)
}

// checkpoint runs one wal_checkpoint(TRUNCATE) on the handle's writer, for
// the times no write runs beside it: shutdown, closing a shard, a cutover copy.
// Both pragmas name main: unqualified, wal_checkpoint would also try every
// attached file, and those are attached read-only and checkpointed by their
// own writer. It returns the WAL size in frames after the attempt (the
// pragma's `log` column), or -1 if it errored. A reader snapshot makes it
// return busy at once with the WAL still in use (see checkpointOnce).
func (d *DB) checkpoint(ctx context.Context, log *slog.Logger) int {
	_, walFrames, _, err := d.checkpointOnce(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("wal checkpoint error", "error", err)
		}
		return -1
	}
	if walFrames == 0 {
		// Reclaim up to 5000 freed pages (~20 MiB). No-op when
		// auto_vacuum != INCREMENTAL (i.e. before migration 018 has run).
		_, _ = d.ExecContext(ctx, "PRAGMA main.incremental_vacuum(5000)")
	}
	return walFrames
}

// checkpointOnce runs one TRUNCATE checkpoint with busy_timeout off on the
// connection it uses, and restores busy_timeout before the connection goes
// back to the pool.
func (d *DB) checkpointOnce(ctx context.Context) (busy, walFrames, checkpointed int, err error) {
	conn, err := d.Conn(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return 0, 0, 0, err
	}
	defer func() {
		if _, rerr := conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMs)); rerr != nil {
			// A connection left without a busy timeout would fail writes
			// with SQLITE_BUSY, so drop it from the pool instead.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			if err == nil {
				err = fmt.Errorf("restore busy_timeout: %w", rerr)
			}
		}
	}()
	err = conn.QueryRowContext(ctx, "PRAGMA main.wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed)
	return busy, walFrames, checkpointed, err
}
