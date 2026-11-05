package repository

import (
	"context"
	"log/slog"
	"time"
)

// The final checkpoint of an ended shard resets its WAL before the handle
// closes. On 2026-11-01 and 2026-11-04 it failed at midnight with no cause
// logged, under a budget of 10s and with no second try (#501, SPA-89). The
// budget and the retry below give it 41s in all, inside the 90s grace period
// even when a shutdown lands on the close.
const (
	// endedCheckpointBudget is the time one attempt may take.
	endedCheckpointBudget = 20 * time.Second
	// endedCheckpointAttempts counts the first try and the retry.
	endedCheckpointAttempts = 2
	// endedCheckpointPause is the wait ahead of the retry.
	endedCheckpointPause = time.Second
	// endedBusyRetry is how often an attempt blocked by a reader snapshot
	// tries again inside its budget.
	endedBusyRetry = 250 * time.Millisecond
)

// endedCheckpoint is the outcome of one final checkpoint attempt.
type endedCheckpoint struct {
	// frames is the WAL size after the attempt, or -1 if it did not run to
	// the end.
	frames int
	// cause is empty when the WAL was reset. Otherwise it is "busy" (a reader
	// snapshot kept frames in the WAL for the whole budget), "timeout" (the
	// budget ran out first, waiting for the connection or inside the
	// TRUNCATE) or "error" (the checkpoint failed; the error itself is in the
	// "wal checkpoint error" warning just before).
	cause string
	times checkpointTimes
}

// checkpointEnded runs one TRUNCATE checkpoint of the handle's file under
// budget, retrying inside it while a reader blocks the reset.
func (d *DB) checkpointEnded(budget time.Duration, log *slog.Logger) endedCheckpoint {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	frames, times := d.checkpointWith(ctx, endedBusyRetry, log, func() {})
	r := endedCheckpoint{frames: frames, times: times}
	switch {
	case frames == 0:
	case frames > 0:
		r.cause = "busy"
	case ctx.Err() != nil:
		r.cause = "timeout"
	default:
		r.cause = "error"
	}
	return r
}

// attrs are the log attributes of attempt on file.
func (r endedCheckpoint) attrs(file string, attempt int) []any {
	return []any{
		"file", file,
		"attempt", attempt,
		"cause", r.cause,
		"wal_frames", r.frames,
		"conn_wait_ms", r.times.connWait.Milliseconds(),
		"truncate_ms", r.times.truncate.Milliseconds(),
		"vacuum_ms", r.times.vacuum.Milliseconds(),
		"checkpoint_tries", r.times.attempts,
	}
}

// finalCheckpoint resets the WAL of the ended shard h, trying once more if
// the first attempt leaves it in place. Only a WAL still in place after the
// retry logs at error level; the message is the one SPA-89 groups on.
func (m *ShardManager) finalCheckpoint(h *DB, file string) {
	budget := m.closeBudget
	if budget == 0 {
		budget = endedCheckpointBudget
	}
	for attempt := 1; ; attempt++ {
		r := h.checkpointEnded(budget, m.logger)
		switch {
		case r.cause == "":
			if attempt > 1 {
				m.logger.Warn("close ended shard: final checkpoint recovered on retry", r.attrs(file, attempt)...)
			}
			return
		case attempt >= endedCheckpointAttempts:
			m.logger.Error("close ended shard: final checkpoint failed", r.attrs(file, attempt)...)
			return
		}
		m.logger.Warn("close ended shard: final checkpoint failed, retrying", r.attrs(file, attempt)...)
		if m.beforeCheckpointRetry != nil {
			m.beforeCheckpointRetry()
		}
		time.Sleep(endedCheckpointPause)
	}
}
