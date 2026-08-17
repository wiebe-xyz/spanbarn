package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func diskFullBatch() []model.SpanRecord {
	return []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "error", DurationUs: 50},
	}
}

// TestDiskFullDoesNotBurnRetries pins the fail-fast behaviour. A full disk is
// not transient: retrying it maxRetries times with quadratic backoff spends
// seconds failing and then dead-letters the batch — throwing away exactly the
// data that would have been written a moment later, once retention frees
// space. One attempt, then out.
func TestDiskFullDoesNotBurnRetries(t *testing.T) {
	repo := &mockRepo{err: errors.New("database or disk is full (13)")}

	rw := &RedisWorker{repo: repo, logger: slog.Default()}
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})
	rw.processBatch(context.Background(), diskFullBatch())

	if got := repo.getCalls(); got != 1 {
		t.Errorf("InsertSpans called %d times for a disk-full error, want 1 — "+
			"a full disk is not transient and retrying it just delays the drop", got)
	}
}

// TestTransientErrorStillRetries is the other half: only disk-full short
// circuits. An ordinary error must still get the full retry budget, or this
// change would quietly make every transient failure fatal.
func TestTransientErrorStillRetries(t *testing.T) {
	// Shrink the backoff so exercising the full retry budget costs
	// milliseconds instead of the ~27s the production values would take.
	orig := insertRetryBackoff
	insertRetryBackoff = time.Millisecond
	t.Cleanup(func() { insertRetryBackoff = orig })

	repo := &mockRepo{err: errors.New("database is locked (5)")}

	rw := &RedisWorker{repo: repo, logger: slog.Default()}
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})
	rw.processBatch(context.Background(), diskFullBatch())

	if got := repo.getCalls(); got != maxRetries {
		t.Errorf("InsertSpans called %d times for a transient error, want %d", got, maxRetries)
	}
}

// TestDiskFullWithoutQueueDoesNotPanic covers the standalone/test wiring where
// no Redis queue exists. The requeue path must degrade to the old
// dead-letter behaviour rather than dereferencing a nil queue.
func TestDiskFullWithoutQueueDoesNotPanic(t *testing.T) {
	repo := &mockRepo{err: errors.New("database or disk is full (13)")}

	rw := &RedisWorker{repo: repo, logger: slog.Default()} // queue is nil
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("processBatch panicked with a nil queue: %v", r)
		}
	}()
	rw.processBatch(context.Background(), diskFullBatch())
}
