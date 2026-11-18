package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fastBusyBackoff(t *testing.T) {
	t.Helper()
	old := mergeBusyBackoff
	mergeBusyBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { mergeBusyBackoff = old })
}

var errBusy = errors.New("merge metric_rollups_coarse: database is locked (5) (SQLITE_BUSY)")

func TestRetryBusyRerunsABusyBatchUntilItPasses(t *testing.T) {
	fastBusyBackoff(t)
	calls := 0
	err := retryBusy(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errBusy
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want nil after 3", err, calls)
	}
}

func TestRetryBusyGivesUpAfterTheLastRetry(t *testing.T) {
	fastBusyBackoff(t)
	calls := 0
	err := retryBusy(context.Background(), func() error { calls++; return errBusy })
	if !errors.Is(err, errBusy) || calls != len(mergeBusyBackoff)+1 {
		t.Fatalf("err = %v after %d calls, want the busy error after %d", err, calls, len(mergeBusyBackoff)+1)
	}
}

func TestRetryBusyReturnsOtherErrorsAtOnce(t *testing.T) {
	fastBusyBackoff(t)
	other := errors.New("no such table")
	calls := 0
	err := retryBusy(context.Background(), func() error { calls++; return other })
	if !errors.Is(err, other) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want the same error after 1", err, calls)
	}
}

func TestRetryBusyStopsWhenTheContextEnds(t *testing.T) {
	fastBusyBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryBusy(ctx, func() error { calls++; return errBusy })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}
