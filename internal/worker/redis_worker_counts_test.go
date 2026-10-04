package worker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// TestRedisWorkerCountsEveryHandledSpan: spans_processed_total reads Counts().
// Boring spans that are aggregated and not stored are handled too, so a batch
// of four with one skipped boring span counts four, and an all-boring batch
// (nothing stored) still counts.
func TestRedisWorkerCountsEveryHandledSpan(t *testing.T) {
	rw := &RedisWorker{repo: &mockRepo{}, logger: slog.Default()}
	rw.SetAccumulator(&mockAccumulator{})
	rw.SetConfig(WorkerConfig{SlowThresholdUs: 1_000_000})

	rw.processBatch(context.Background(), []model.SpanRecord{
		{ProjectID: 1, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc", Status: "OK", DurationUs: 50},
		{ProjectID: 1, TraceID: "t2", SpanID: "s2", Name: "op", Service: "svc", Status: "error", DurationUs: 50},
		{ProjectID: 1, TraceID: "t2", SpanID: "s3", Name: "op", Service: "svc", Status: "OK", DurationUs: 50},
		{ProjectID: 1, TraceID: "t3", SpanID: "s4", Name: "op", Service: "svc", Status: "OK", DurationUs: 2_000_000},
	})
	if processed, failed := rw.Counts(); processed != 4 || failed != 0 {
		t.Fatalf("after mixed batch: processed=%d failed=%d, want 4/0", processed, failed)
	}

	rw.processBatch(context.Background(), []model.SpanRecord{
		{ProjectID: 1, TraceID: "t9", SpanID: "s9", Name: "op", Service: "svc", Status: "OK", DurationUs: 50},
	})
	if processed, _ := rw.Counts(); processed != 5 {
		t.Fatalf("after all-boring batch: processed=%d, want 5", processed)
	}
}
