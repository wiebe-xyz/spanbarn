package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// stagedThenCancel stops the worker once a batch reaches staging, so Run returns
// with no sleep to race against.
type stagedThenCancel struct {
	mockRepo
	cancel context.CancelFunc
}

func (r *stagedThenCancel) InsertSpansStaging(ctx context.Context, spans []repository.Span) error {
	defer r.cancel()
	return r.mockRepo.InsertSpansStaging(ctx, spans)
}

type panicOnStage struct{ mockRepo }

func (r *panicOnStage) InsertSpansStaging(context.Context, []repository.Span) error {
	panic("batch cannot be stored")
}

func reclaimBatch(t *testing.T, spanIDs ...string) string {
	t.Helper()
	records := make([]model.SpanRecord, 0, len(spanIDs))
	for _, id := range spanIDs {
		records = append(records, model.SpanRecord{ProjectID: 1, TraceID: "t1", SpanID: id, Name: "op", Service: "svc"})
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func reclaimWorker(t *testing.T, repo Repository) (*RedisWorker, *miniredis.Miniredis, *bytes.Buffer) {
	t.Helper()
	mr := miniredis.RunT(t)
	q, err := queue.NewRedisQueueWithRetry(context.Background(), "redis://"+mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	var logs bytes.Buffer
	rw := NewRedisWorker(q, repo, slog.New(slog.NewTextHandler(&logs, nil)))
	rw.SetStagingMode(true)
	return rw, mr, &logs
}

// A deleted or evicted writer pod is replaced at once while the old one drains
// its last batch. The new writer must leave that batch alone during the delay,
// or both writers store it.
func TestRunLeavesTheProcessingListAloneDuringTheDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &stagedThenCancel{cancel: cancel}
	rw, mr, logs := reclaimWorker(t, repo)
	started := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rw.now = func() time.Time { return started }

	drainingBatch := reclaimBatch(t, "old")
	mr.Lpush(queue.WriteQueueProcessingKey, drainingBatch)
	mr.Lpush(queue.WriteQueueRecentKey, reclaimBatch(t, "new"))

	rw.Run(ctx)

	spans := repo.getSpans()
	if len(spans) != 1 || spans[0].SpanID != "new" {
		t.Fatalf("stored %+v, want only the span from the queue", spans)
	}
	held, _ := mr.List(queue.WriteQueueProcessingKey)
	if len(held) != 1 || held[0] != drainingBatch {
		t.Errorf("processing list = %v, want the draining writer's batch untouched", held)
	}
	if strings.Contains(logs.String(), "reclaimed") {
		t.Errorf("logged a reclaim during the delay:\n%s", logs)
	}
}

// Past the delay the batch is an orphan: it gets stored, and the warning carries
// the span count that ties a later ledger gap to it.
func TestRunReclaimsAfterTheDelayAndLogsTheCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	repo := &stagedThenCancel{cancel: cancel}
	rw, mr, logs := reclaimWorker(t, repo)
	started := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	calls := 0
	rw.now = func() time.Time {
		calls++
		if calls == 1 {
			return started
		}
		return started.Add(queue.ReclaimDelay)
	}

	mr.Lpush(queue.WriteQueueProcessingKey, reclaimBatch(t, "s1", "s2", "s3"))

	rw.Run(ctx)

	if got := len(repo.getSpans()); got != 3 {
		t.Fatalf("stored %d spans, want the 3 reclaimed", got)
	}
	// The cancel landed before the removal, as a shutdown does, and the batch
	// still left the list.
	if mr.Exists(queue.WriteQueueProcessingKey) || mr.Exists(queue.WriteQueueRecentKey) {
		t.Error("batch still on a list after it was stored")
	}
	line := logs.String()
	if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "reclaimed") ||
		!strings.Contains(line, "batches=1") || !strings.Contains(line, "spans=3") {
		t.Errorf("want a warning with batches=1 and spans=3, got:\n%s", line)
	}
}

// Before the processing list a batch that panicked was gone and the next start
// worked. Kept on the list, it would stop the worker again on every start.
func TestABatchThatPanicsLeavesTheProcessingList(t *testing.T) {
	rw, mr, _ := reclaimWorker(t, &panicOnStage{})
	mr.Lpush(queue.WriteQueueRecentKey, reclaimBatch(t, "s1"))

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach Run's caller")
			}
		}()
		rw.Run(context.Background())
	}()

	if mr.Exists(queue.WriteQueueProcessingKey) {
		t.Error("the batch that panicked is still on the processing list")
	}
}
