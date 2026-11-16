package engine

import (
	"context"
	"log/slog"
	"time"
)

// endedBusyRetry is how often an attempt blocked by a reader snapshot tries
// again inside its budget.
const endedBusyRetry = 250 * time.Millisecond

// EndedCheckpoint is the outcome of one final checkpoint attempt.
type EndedCheckpoint struct {
	// Frames is the WAL size after the attempt, or -1 if it did not run to
	// the end.
	Frames int
	// Cause is empty when the WAL was reset. Otherwise it is "busy" (a reader
	// snapshot kept frames in the WAL for the whole budget), "timeout" (the
	// budget ran out first, waiting for the connection or inside the
	// TRUNCATE) or "error" (the checkpoint failed; the error itself is in the
	// "wal checkpoint error" warning just before).
	Cause string
	times checkpointTimes
}

// CheckpointEnded runs one TRUNCATE checkpoint of the handle's file under
// budget, retrying inside it while a reader blocks the reset.
func (d *DB) CheckpointEnded(budget time.Duration, log *slog.Logger) EndedCheckpoint {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	frames, times := d.checkpointWith(ctx, endedBusyRetry, log, func() {})
	r := EndedCheckpoint{Frames: frames, times: times}
	switch {
	case frames == 0:
	case frames > 0:
		r.Cause = "busy"
	case ctx.Err() != nil:
		r.Cause = "timeout"
	default:
		r.Cause = "error"
	}
	return r
}

// Attrs are the log attributes of attempt on file.
func (r EndedCheckpoint) Attrs(file string, attempt int) []any {
	return []any{
		"file", file,
		"attempt", attempt,
		"cause", r.Cause,
		"wal_frames", r.Frames,
		"conn_wait_ms", r.times.connWait.Milliseconds(),
		"truncate_ms", r.times.truncate.Milliseconds(),
		"vacuum_ms", r.times.vacuum.Milliseconds(),
		"checkpoint_tries", r.times.attempts,
	}
}
