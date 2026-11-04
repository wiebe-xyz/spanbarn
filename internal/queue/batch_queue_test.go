package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func rawRecords[T any](t *testing.T, records []T) string {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// testBatchQueue runs one queue through the claim, ack and reclaim paths.
func testBatchQueue[T any](t *testing.T, key string, open func(*RedisQueue) *BatchQueue[T], records []T) {
	t.Helper()
	ctx := context.Background()
	processing := key + processingSuffix
	raw := rawRecords(t, records)

	// A BRPOP reply lost after the server popped the item lost the whole batch.
	t.Run("lost reply leaves the batch recoverable", func(t *testing.T) {
		q, mr := newTestQueue(t)
		mr.Lpush(key, raw)
		q.client.AddHook(&lostReply{})
		b := open(q)

		if _, err := b.Consume(ctx); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Consume error = %v, want the read deadline", err)
		}
		got, err := b.Reclaim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got != (Reclaimed{Batches: 1, Records: len(records)}) {
			t.Fatalf("Reclaim = %+v, want 1 batch of %d: the batch was lost", got, len(records))
		}
		batch, err := b.Consume(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if batch == nil || len(batch.Records) != len(records) {
			t.Fatalf("Consume after reclaim = %+v, want the batch back whole", batch)
		}
	})

	t.Run("the batch stays on the processing list until Ack", func(t *testing.T) {
		q, mr := newTestQueue(t)
		mr.Lpush(key, raw)
		b := open(q)

		batch, err := b.Consume(ctx)
		if err != nil || batch == nil {
			t.Fatalf("Consume = %+v, %v", batch, err)
		}
		if mr.Exists(key) || !mr.Exists(processing) {
			t.Fatal("the consumed batch should be on the processing list only")
		}
		if err := b.Ack(ctx, batch); err != nil {
			t.Fatal(err)
		}
		if mr.Exists(processing) {
			t.Error("batch still on the processing list after Ack")
		}
		if got, err := b.Reclaim(ctx); err != nil || got.Batches != 0 {
			t.Errorf("Reclaim after Ack = %+v, %v, want nothing to replay", got, err)
		}
	})

	// The reclaimed batch is read first: it is older than the rest of the queue.
	t.Run("a reclaimed batch is read ahead of the queue", func(t *testing.T) {
		q, mr := newTestQueue(t)
		mr.Lpush(processing, raw)
		mr.Lpush(key, rawRecords(t, records[:1]))
		b := open(q)

		if _, err := b.Reclaim(ctx); err != nil {
			t.Fatal(err)
		}
		batch, err := b.Consume(ctx)
		if err != nil || batch == nil {
			t.Fatalf("Consume = %+v, %v", batch, err)
		}
		if len(batch.Records) != len(records) {
			t.Errorf("read %d records first, want the reclaimed %d", len(batch.Records), len(records))
		}
	})

	t.Run("an item that does not parse leaves the list", func(t *testing.T) {
		q, mr := newTestQueue(t)
		mr.Lpush(key, "not json")
		b := open(q)

		if _, err := b.Consume(ctx); err == nil {
			t.Fatal("Consume of an unparsable item returned no error")
		}
		if mr.Exists(processing) {
			t.Error("an item that cannot parse stays on the processing list and replays forever")
		}
	})
}

func TestLogsQueueClaimAckReclaim(t *testing.T) {
	testBatchQueue(t, LogsQueueKey, (*RedisQueue).Logs, []model.LogRecord{{ProjectID: 1, Body: "a"}, {ProjectID: 1, Body: "b"}})
}

func TestMetricsQueueClaimAckReclaim(t *testing.T) {
	testBatchQueue(t, MetricsQueueKey, (*RedisQueue).Metrics, []model.MetricRecord{{ProjectID: 1, Name: "a"}, {ProjectID: 1, Name: "b"}})
}

func TestSpanLedgerQueueClaimAckReclaim(t *testing.T) {
	testBatchQueue(t, SpanLedgerQueueKey, (*RedisQueue).SpanLedger, []model.SpanLedgerRow{{ProjectID: 1, Accepted: 1}, {ProjectID: 2, Accepted: 2}})
}

// Touches may be lost, so that queue has no processing list.
func TestTouchQueueKeepsNoProcessingList(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestQueue(t)
	if err := q.PublishAPIKeyTouches(ctx, []int64{7}); err != nil {
		t.Fatal(err)
	}
	ids, err := q.ConsumeAPIKeyTouches(ctx)
	if err != nil || len(ids) != 1 {
		t.Fatalf("ConsumeAPIKeyTouches = %v, %v", ids, err)
	}
	for _, key := range mr.Keys() {
		t.Errorf("key %q left behind after the touch was consumed", key)
	}
}
