package forward

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spanledger"
)

var errQueueFull = errors.New("queue: lpush: OOM command not allowed when used memory > 'maxmemory'")

func droppingForwarder(t *testing.T, n int) (*RedisForwarder, *spanledger.Tally, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	tally := spanledger.New()
	f := &RedisForwarder{
		spool:  spoolWith(t, n),
		queue:  &fakePublisher{fail: errQueueFull},
		logger: slog.New(slog.NewTextHandler(&logs, nil)),
		ledger: tally,
	}
	return f, tally, &logs
}

func failedSpans(t *testing.T, tally *spanledger.Tally) int64 {
	t.Helper()
	var failed int64
	err := tally.Report(context.Background(), func(_ context.Context, rows []model.SpanLedgerRow) error {
		for _, r := range rows {
			failed += r.Failed
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	return failed
}

// A batch Redis refuses is gone, so the ledger has to hold it. It read as a gap
// with no cause before, and only once the bucket settled.
func TestRedisForwarderCountsADroppedBatchAsFailed(t *testing.T) {
	n := 2*defaultBatchSize + 7
	f, tally, _ := droppingForwarder(t, n)

	got, err := f.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got != n {
		t.Errorf("Drain moved past %d spans, want %d", got, n)
	}
	if failed := failedSpans(t, tally); failed != int64(n) {
		t.Errorf("ledger failed = %d, want %d", failed, n)
	}
}

// Every batch fails at maxmemory, so the ERROR line carries a total and files
// one BugBarn event per interval.
func TestRedisForwarderCapsTheDropLine(t *testing.T) {
	const batches = 20
	f, _, logs := droppingForwarder(t, batches*defaultBatchSize)
	ctx := context.Background()

	cursor := int64(0)
	for i := 0; i < batches; i++ {
		next, n, err := f.forwardBatch(ctx, cursor)
		if err != nil || n != defaultBatchSize {
			t.Fatalf("forwardBatch %d: n=%d err=%v", i, n, err)
		}
		cursor = next
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 1 {
		t.Fatalf("ERROR lines within the interval = %d, want 1:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "batches=1 spans=500") {
		t.Errorf("first line does not name the first drop:\n%s", logs.String())
	}

	// The rest goes out once the interval has passed, also with nothing to drop.
	logs.Reset()
	f.lastDropReport = time.Now().Add(-dropReportInterval)
	if _, _, err := f.forwardBatch(ctx, cursor); err != nil {
		t.Fatalf("forwardBatch: %v", err)
	}
	if !strings.Contains(logs.String(), "batches=19 spans=9500") {
		t.Errorf("second line does not hold the total since the first:\n%s", logs.String())
	}
}

// Shutdown reports what is left, because no later tick will.
func TestRedisForwarderDrainReportsTheRemainingDrops(t *testing.T) {
	f, _, logs := droppingForwarder(t, 3*defaultBatchSize)

	if _, err := f.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := strings.Count(logs.String(), "level=ERROR"); got != 2 {
		t.Fatalf("ERROR lines = %d, want the first drop and the last report:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "batches=2 spans=1000") {
		t.Errorf("last report does not hold the rest:\n%s", logs.String())
	}
}

// Any other publish failure keeps the batch, so nothing is lost or counted.
func TestRedisForwarderKeepsABatchOnOtherPublishErrors(t *testing.T) {
	f, tally, _ := droppingForwarder(t, 3)
	f.queue = &fakePublisher{fail: errors.New("connection refused")}

	if _, _, err := f.forwardBatch(context.Background(), 0); err == nil {
		t.Fatal("forwardBatch returned no error")
	}
	if failed := failedSpans(t, tally); failed != 0 {
		t.Errorf("ledger failed = %d, want 0", failed)
	}
}
