package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func newTestQueue(t *testing.T) (*RedisQueue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	q, err := NewRedisQueueWithRetry(context.Background(), "redis://"+mr.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return q, mr
}

func testBatch(t *testing.T, spanIDs ...string) string {
	t.Helper()
	records := make([]model.SpanRecord, 0, len(spanIDs))
	for _, id := range spanIDs {
		records = append(records, model.SpanRecord{ProjectID: 1, TraceID: "t1", SpanID: id})
	}
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// lostReply lets the first pop run on the server and then fails it the way a
// read deadline does: the server has applied the command and the client never
// sees the reply. go-redis does not retry that on a blocking command, so the
// caller gets the error. before runs ahead of the command it is about to lose.
type lostReply struct {
	before func(cmd string)
	lost   bool
}

func (h *lostReply) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *lostReply) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h *lostReply) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.before != nil {
			h.before(cmd.Name())
		}
		err := next(ctx, cmd)
		if h.lost || err != nil || !isPop(cmd.Name()) {
			return err
		}
		h.lost = true
		timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
		cmd.SetErr(timeout)
		return timeout
	}
}

func isPop(name string) bool {
	return name == "lmove" || name == "blmove" || name == "brpop"
}

// A reply lost after the server popped the item used to lose the whole batch:
// BRPOP had removed it and the writer logged a transient consume failure. The
// batch must stay recoverable, and come back whole.
func TestConsumeLostReplyLeavesTheBatchRecoverable(t *testing.T) {
	ctx := context.Background()
	for name, seed := range map[string]func(mr *miniredis.Miniredis, raw string) func(string){
		"recent queue": func(mr *miniredis.Miniredis, raw string) func(string) {
			mr.Lpush(WriteQueueRecentKey, raw)
			return nil
		},
		"backlog queue": func(mr *miniredis.Miniredis, raw string) func(string) {
			mr.Lpush(WriteQueueBacklogKey, raw)
			return nil
		},
		"blocking wait": func(mr *miniredis.Miniredis, raw string) func(string) {
			// The batch lands once the writer has started to block, so the
			// blocking move is the one that loses its reply.
			return func(cmd string) {
				if cmd == "blmove" {
					mr.Lpush(WriteQueueRecentKey, raw)
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			q, mr := newTestQueue(t)
			raw := testBatch(t, "s1", "s2")
			q.client.AddHook(&lostReply{before: seed(mr, raw)})

			if _, err := q.Consume(ctx); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("Consume error = %v, want the read deadline", err)
			}

			got, err := q.Reclaim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != (Reclaimed{Batches: 1, Spans: 2}) {
				t.Fatalf("Reclaim = %+v, want 1 batch of 2 spans: the batch was lost", got)
			}

			batch, err := q.Consume(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if batch == nil || len(batch.Records) != 2 {
				t.Fatalf("Consume after reclaim = %+v, want the 2 spans back", batch)
			}
			if err := q.Ack(ctx, batch); err != nil {
				t.Fatal(err)
			}
			if mr.Exists(WriteQueueProcessingKey) || mr.Exists(WriteQueueRecentKey) {
				t.Error("batch still on a list after Ack")
			}
		})
	}
}

func TestConsumeKeepsTheBatchUntilAck(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestQueue(t)
	mr.Lpush(WriteQueueRecentKey, testBatch(t, "s1"))

	batch, err := q.Consume(ctx)
	if err != nil || batch == nil {
		t.Fatalf("Consume = %+v, %v", batch, err)
	}
	if held, _ := mr.List(WriteQueueProcessingKey); len(held) != 1 {
		t.Fatalf("processing list holds %d batches before Ack, want 1", len(held))
	}
	if err := q.Ack(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if mr.Exists(WriteQueueProcessingKey) {
		t.Error("processing list still holds the batch after Ack")
	}
}

func TestConsumeTakesRecentBeforeBacklog(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestQueue(t)
	mr.Lpush(WriteQueueBacklogKey, testBatch(t, "old"))
	mr.Lpush(WriteQueueRecentKey, testBatch(t, "new"))

	for _, want := range []string{"new", "old"} {
		batch, err := q.Consume(ctx)
		if err != nil || batch == nil {
			t.Fatalf("Consume = %+v, %v", batch, err)
		}
		if got := batch.Records[0].SpanID; got != want {
			t.Errorf("Consume returned span %q, want %q", got, want)
		}
	}
}

// The writer's idle loop lands here on every block that times out.
func TestConsumeOnEmptyQueuesReturnsNothing(t *testing.T) {
	q, _ := newTestQueue(t)
	batch, err := q.Consume(context.Background())
	if batch != nil || err != nil {
		t.Fatalf("Consume = %+v, %v, want nil, nil", batch, err)
	}
}

// A batch that does not parse never will, so it must not come back on reclaim.
func TestConsumeDropsABatchThatDoesNotParse(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestQueue(t)
	mr.Lpush(WriteQueueRecentKey, "not json")

	if _, err := q.Consume(ctx); err == nil {
		t.Fatal("Consume error = nil, want the parse failure")
	}
	if got, err := q.Reclaim(ctx); err != nil || got.Batches != 0 {
		t.Errorf("Reclaim = %+v, %v, want nothing left", got, err)
	}
}

func TestReclaimReturnsEveryBatchLeftBehind(t *testing.T) {
	ctx := context.Background()
	q, mr := newTestQueue(t)
	mr.Lpush(WriteQueueProcessingKey, testBatch(t, "s1"))
	mr.Lpush(WriteQueueProcessingKey, testBatch(t, "s2", "s3"))
	mr.Lpush(WriteQueueRecentKey, testBatch(t, "s4"))

	got, err := q.Reclaim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Reclaimed{Batches: 2, Spans: 3}) {
		t.Errorf("Reclaim = %+v, want 2 batches of 3 spans", got)
	}
	if queued, _ := mr.List(WriteQueueRecentKey); len(queued) != 3 {
		t.Errorf("recent queue holds %d batches, want 3", len(queued))
	}
	// A reclaimed batch goes ahead of what is waiting.
	batch, err := q.Consume(ctx)
	if err != nil || batch == nil {
		t.Fatalf("Consume = %+v, %v", batch, err)
	}
	if batch.Records[0].SpanID == "s4" {
		t.Error("the waiting batch came before the reclaimed ones")
	}
}
