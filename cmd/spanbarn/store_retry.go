package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// A logs or metrics batch whose insert failed was finished after one attempt,
// so a disk stall of a few seconds lost its lines or its raw points (#496).
// storeWithRetry gives these inserts the span worker's treatment: a transient
// failure is retried with quadratic backoff, and a full disk waits for retention
// to free space, which it does within a cycle.

// storeAttempts is how many transient failures an insert gets before its batch
// is given up.
const storeAttempts = 5

var (
	// storeRetryBackoff times the failure count squared is the wait after a
	// transient failure: 0.5, 2, 4.5 and 8 seconds, 15 seconds in all.
	storeRetryBackoff = 500 * time.Millisecond
	// storeDiskFullWait is the wait between attempts while the disk is full.
	storeDiskFullWait = 5 * time.Second
)

// storeWithRetry runs insert until it succeeds, fails storeAttempts times with
// a transient error, or ctx is cancelled, and returns the last error. A full
// disk does not count as a failure, so it holds the batch until retention frees
// space. insert runs on a context a shutdown does not cancel; a shutdown ends
// only the waits between attempts.
func storeWithRetry(ctx context.Context, logger *slog.Logger, name string, records int, insert func(context.Context) error) error {
	failures := 0
	diskFull := false
	for attempt := 1; ; attempt++ {
		err := insert(context.WithoutCancel(ctx))
		if err == nil {
			if diskFull {
				logger.Info(name+" insert stored after the disk was full", "records", records, "attempts", attempt)
			}
			return nil
		}
		wait := storeDiskFullWait
		if engine.IsDiskFull(err) {
			if !diskFull {
				diskFull = true
				logger.Error(name+" insert: disk full, holding the batch until retention frees space", "records", records, "error", err)
			}
		} else {
			failures++
			if failures >= storeAttempts {
				return err
			}
			logger.Info(name+" insert attempt failed, retrying", "attempt", attempt, "records", records, "error", err)
			wait = time.Duration(failures*failures) * storeRetryBackoff
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}
