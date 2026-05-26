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

// Pragmas are applied via DSN query params (_pragma=...) so they take effect on
// every connection the pool opens — not just the first one. Setting them via
// db.Exec("PRAGMA …") only configures the single connection that handled the
// Exec call; later connections spawned under load inherit no pragmas, so
// busy_timeout defaults to 0 and any contention surfaces as SQLITE_BUSY.
func buildDSN(dbPath string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
		// Disable automatic passive checkpoints; the writer runs explicit TRUNCATE
		// checkpoints on a fixed interval instead. Passive checkpoints silently
		// stop at any reader snapshot boundary, so they cannot prevent unbounded
		// WAL growth under sustained read load.
		q.Add("_pragma", "wal_autocheckpoint(0)")
	}
	if readOnly {
		q.Set("mode", "ro")
	}
	prefix := "file:"
	if !strings.HasPrefix(dbPath, "file:") && dbPath != ":memory:" {
		dbPath = prefix + dbPath
	}
	return dbPath + "?" + q.Encode()
}

// NewDB opens a SQLite database at dbPath with WAL mode, busy timeout, and foreign keys enabled.
// MaxOpenConns is capped at 1: SQLite allows only one writer at a time, and when two goroutines
// each hold a separate connection and try to upgrade a deferred transaction to a write lock they
// deadlock each other — SQLite returns SQLITE_BUSY immediately, before busy_timeout can help.
// Serialising through one connection means the second writer waits in Go's pool (not in SQLite).
func NewDB(dbPath string) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, false), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	// Expose connection pool stats as OTel metrics (best-effort).
	_, _ = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(semconv.DBSystemSqlite))
	return &DB{DB: db}, nil
}

// NewReadOnlyDB opens an existing SQLite database at dbPath in read-only mode.
// Safe to use concurrently with a writer process on the same file when WAL mode
// is active on that file.
func NewReadOnlyDB(dbPath string) (*DB, error) {
	db, err := otelsql.Open("sqlite", buildDSN(dbPath, true), otelOpts...)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	return &DB{DB: db}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}

// RunPeriodicCheckpoint blocks until ctx is cancelled, issuing a WAL TRUNCATE
// checkpoint on each tick. When a checkpoint returns busy=1 (a reader snapshot
// blocks full WAL backfill), it retries every retryInterval until the readers
// release or the next full-interval tick arrives.
//
// Note: TRUNCATE mode does NOT automatically retry via busy_timeout when readers
// block WAL backfill — it returns SQLITE_BUSY immediately in that phase. busy_timeout
// only applies to write-lock conflicts between concurrent writers. Application-level
// retry (done here) is required to eventually drain the WAL under read load.
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
// Long-horizon plan: hand off WAL maintenance to Litestream, the same way
// BugBarn and FunnelBarn do. Once Litestream is doing the periodic checkpoint
// as part of its replication loop, this goroutine can go away entirely and
// "snapshots are not the writer's problem" becomes literally true.
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

// FinalCheckpoint runs one WAL TRUNCATE checkpoint on a fresh context. Call
// after all writers have stopped (e.g. after wg.Wait()) and before db.Close(),
// so the WAL is merged into the main file on every clean shutdown.
// With wal_autocheckpoint(0), db.Close() does not checkpoint automatically.
func (d *DB) FinalCheckpoint(log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.checkpoint(ctx, 0, log)
}

func (d *DB) checkpoint(ctx context.Context, retryInterval time.Duration, log *slog.Logger) {
	for {
		var busy, walFrames, checkpointed int
		if err := d.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
			if ctx.Err() == nil {
				log.Warn("wal checkpoint error", "error", err)
			}
			return
		}
		if busy == 0 {
			// Reclaim up to 5000 freed pages (~20 MiB) per tick. No-op when
			// auto_vacuum != INCREMENTAL (i.e. before migration 018 has run).
			_, _ = d.ExecContext(ctx, "PRAGMA incremental_vacuum(5000)")
			return
		}
		// busy=1: a reader snapshot is blocking full WAL backfill. Retry after a
		// short interval so the WAL can be drained once the reader finishes.
		log.Debug("wal checkpoint blocked by reader, retrying", "wal_frames", walFrames, "checkpointed", checkpointed)
		if retryInterval == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}
