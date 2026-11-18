package repository

import (
	"context"
	"strings"
	"time"
)

// mergeBusyBackoff is the wait before each retry of a merge batch that hit a
// busy database: six retries, about a minute in all. A variable so a test runs
// it in milliseconds.
var mergeBusyBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// isBusyError reports a SQLITE_BUSY or SQLITE_LOCKED failure. The driver puts
// the text into the error and exposes no typed value across the pool.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database table is locked")
}

// retryBusy runs fn and runs it again after a pause while it fails with a busy
// error, up to the length of mergeBusyBackoff retries. A batch is one
// transaction that rolled back, so a rerun starts from the same rows. Any other
// error, and a busy error after the last retry, is returned as it came.
func retryBusy(ctx context.Context, fn func() error) error {
	err := fn()
	for _, wait := range mergeBusyBackoff {
		if !isBusyError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		err = fn()
	}
	return err
}
