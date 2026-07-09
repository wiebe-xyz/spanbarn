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
		// Disable SQLite's automatic passive checkpoints; the writer issues
		// explicit checkpoints on a fixed interval instead — TRUNCATE when running
		// standalone, or PASSIVE when a Litestream replica is attached (see
		// RunPeriodicCheckpoint / CheckpointMode). SQLite's own automatic passive
		// checkpoints silently stop at any reader snapshot boundary, so they cannot
		// prevent unbounded WAL growth under sustained read load.
		q.Add("_pragma", "wal_autocheckpoint(0)")
		// Keep the index working set resident. The spans table carries ~12
		// indexes, so every insert traverses many B-trees; with the SQLite
		// default 2 MiB cache against a ~900 MB DB those pages were evicted and
		// pread back from disk on every batch, collapsing write throughput below
		// the ingest rate and backing up the redis write-queue. A 256 MiB page
		// cache holds the hot interior pages; the mmap window serves the rest
		// from the OS page cache (reclaimable) instead of per-page pread.
		// Sized to fit under the writer pod's GOMEMLIMIT / memory limit.
		q.Add("_pragma", "cache_size(-262144)")   // 256 MiB (negative = KiB)
		q.Add("_pragma", "mmap_size(2147483648)") // 2 GiB
	}
	if readOnly {
		q.Set("mode", "ro")
		// Reader/query handles run in memory-constrained pods, so keep the page
		// cache modest (still 16x the SQLite default). The mmap window is OS page
		// cache and reclaimable under pressure, so it speeds up dashboard/alert
		// reads without growing the Go heap.
		q.Add("_pragma", "cache_size(-32768)")    // 32 MiB
		q.Add("_pragma", "mmap_size(1073741824)") // 1 GiB
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

// CheckpointMode selects the WAL checkpoint strategy the writer issues.
//
//   - CheckpointTruncate resets the WAL to zero bytes on each tick. It bounds WAL
//     size aggressively, but RESTARTS the WAL header — which a co-resident
//     Litestream replica reads as "wal truncated by another process" and answers
//     by starting a new generation and re-uploading a full snapshot (multi-minute,
//     lock-contending). Use only when no Litestream replica is attached.
//   - CheckpointPassive folds committed frames into the main DB without resetting
//     the WAL header, so an attached Litestream keeps streaming the same generation
//     (no forced re-snapshot). Litestream performs its own WAL restarts as part of
//     replication; the writer's PASSIVE pass is the reliable flush that keeps the
//     WAL bounded even when Litestream's own checkpoint loses the write-lock race.
type CheckpointMode string

const (
	CheckpointTruncate CheckpointMode = "TRUNCATE"
	CheckpointPassive  CheckpointMode = "PASSIVE"
)

// RunPeriodicCheckpoint blocks until ctx is cancelled, issuing a WAL checkpoint
// in the given mode on each tick. In TRUNCATE mode, when a checkpoint returns
// busy=1 (a reader snapshot blocks full WAL backfill) it retries every
// retryInterval until the readers release or the next full-interval tick arrives;
// PASSIVE mode never escalates, so it simply waits for the next tick.
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
// truncateAboveFrames, when > 0 and mode is PASSIVE, escalates to a one-off
// TRUNCATE on any tick where the WAL still holds more than that many frames
// after the PASSIVE pass. This bounds the WAL under sustained read load (PASSIVE
// cannot reset it past the oldest reader snapshot) at the cost of an occasional
// Litestream re-snapshot — only when the WAL actually grows large, not every tick.
//
// The checkpoint runs unconditionally on every tick. An earlier version skipped
// checkpoints while the Redis write-queue backlog was deep, on the theory that a
// checkpoint competes with backlog-draining writes on the single connection. In
// production the stale backlog kept that gate tripped permanently, so no
// checkpoint ever ran and the WAL bloated to hundreds of MB — which made *every*
// write slow (each op walks the WAL), the exact opposite of the intended effect,
// and even stalled a schema migration on first open. PASSIVE checkpoints are
// cheap; running them every tick keeps the WAL small and writes fast, so there is
// no gate.
func (d *DB) RunPeriodicCheckpoint(ctx context.Context, interval time.Duration, mode CheckpointMode, truncateAboveFrames int, log *slog.Logger) {
	retryInterval := 5 * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			frames := d.checkpoint(ctx, mode, retryInterval, log)
			if mode == CheckpointPassive && truncateAboveFrames > 0 && frames > truncateAboveFrames {
				log.Info("wal exceeded truncate threshold; escalating to one TRUNCATE checkpoint",
					"wal_frames", frames, "threshold_frames", truncateAboveFrames)
				d.checkpoint(ctx, CheckpointTruncate, retryInterval, log)
			}
		}
	}
}

// FinalCheckpoint runs one WAL checkpoint (in the given mode) on a fresh context.
// Call after all writers have stopped (e.g. after wg.Wait()) and before
// db.Close(), so the WAL is merged into the main file on every clean shutdown.
// With wal_autocheckpoint(0), db.Close() does not checkpoint automatically.
func (d *DB) FinalCheckpoint(mode CheckpointMode, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d.checkpoint(ctx, mode, 0, log)
}

// checkpoint runs one wal_checkpoint(mode) and returns the WAL size in frames
// after the attempt (the pragma's `log` column), or -1 if it errored. In
// TRUNCATE mode it retries on busy=1 (reader blocking backfill); PASSIVE returns
// after a single pass.
func (d *DB) checkpoint(ctx context.Context, mode CheckpointMode, retryInterval time.Duration, log *slog.Logger) int {
	for {
		var busy, walFrames, checkpointed int
		if err := d.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+string(mode)+")").Scan(&busy, &walFrames, &checkpointed); err != nil {
			if ctx.Err() == nil {
				log.Warn("wal checkpoint error", "mode", string(mode), "error", err)
			}
			return -1
		}
		if busy == 0 {
			// Reclaim up to 5000 freed pages (~20 MiB) per tick. No-op when
			// auto_vacuum != INCREMENTAL (i.e. before migration 018 has run).
			_, _ = d.ExecContext(ctx, "PRAGMA incremental_vacuum(5000)")
			return walFrames
		}
		// busy=1: a reader snapshot is blocking full WAL backfill. PASSIVE has
		// already flushed every frame it could this pass and does not escalate, so
		// there is nothing to gain by retrying within the tick — wait for the next
		// interval. TRUNCATE retries so the WAL is actually reset once the reader
		// releases.
		if mode != CheckpointTruncate {
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
