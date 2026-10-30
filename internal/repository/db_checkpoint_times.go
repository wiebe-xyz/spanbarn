package repository

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"
)

// slowCheckpoint is the time at or above which a periodic checkpoint logs its
// phases. Below it a checkpoint is routine, and at 30s ticks on several files a
// line for each one would outnumber the rest of the writer's log.
const slowCheckpoint = 500 * time.Millisecond

// checkpointTimes is where one periodic checkpoint spent its time. The stored
// writescheduler.job spans cannot show this: a checkpoint holds the writer
// connection outside the write scheduler, so the time lands on whichever job
// started next, as duration with no wait (#431, #370).
type checkpointTimes struct {
	// backfill is the PASSIVE pre-pass on the checkpoint connection. Writes go
	// on beside it.
	backfill time.Duration
	// connWait is the time spent waiting for the writer connection. It holds
	// nothing, so it does not count towards slowCheckpoint.
	connWait time.Duration
	// truncate is the TRUNCATE on the writer connection. Every write to the
	// file waits for it.
	truncate time.Duration
	// vacuum is the incremental vacuum after the reset, also on the writer
	// connection, including any wait for it.
	vacuum   time.Duration
	attempts int
}

// log writes one line when the checkpoint of file took slowCheckpoint or longer.
func (t checkpointTimes) log(log *slog.Logger, file string, walFrames int) {
	if t.backfill+t.truncate+t.vacuum < slowCheckpoint {
		return
	}
	log.Info("slow wal checkpoint",
		"file", filepath.Base(file),
		"backfill_ms", t.backfill.Milliseconds(),
		"conn_wait_ms", t.connWait.Milliseconds(),
		"truncate_ms", t.truncate.Milliseconds(),
		"vacuum_ms", t.vacuum.Milliseconds(),
		"attempts", t.attempts,
		"wal_frames", walFrames,
	)
}

// vacuumFreed reclaims up to 5000 freed pages (~20 MiB). It is a no-op when
// auto_vacuum != INCREMENTAL (i.e. before migration 018 has run).
func (d *DB) vacuumFreed(ctx context.Context, times *checkpointTimes) {
	start := time.Now()
	_, _ = d.ExecContext(ctx, "PRAGMA main.incremental_vacuum(5000)")
	times.vacuum += time.Since(start)
}
