package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// insertTelemetry writes 2 metric points, 3 log records and 1 prompt record.
func insertTelemetry(t *testing.T, repo *Repository) {
	t.Helper()
	ctx := context.Background()
	if err := repo.InsertMetrics(ctx, []model.MetricRecord{
		makeMetricRecord(1, "cpu", model.MetricTypeGauge, 1),
		makeMetricRecord(1, "mem", model.MetricTypeGauge, 2),
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if err := repo.InsertLogs(ctx, []model.LogRecord{
		makeLogRecord(1, "t", "a", 9), makeLogRecord(1, "t", "b", 9), makeLogRecord(1, "t", "c", 9),
	}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
	if err := repo.InsertPromptRecords([]PromptRecord{{ProjectID: 1, TraceID: "t", SpanID: "s", Name: "chat"}}); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}
}

func checkIngestRows(t *testing.T, got map[string][]int64) {
	t.Helper()
	want := map[string]int64{"InsertMetrics": 2, "InsertLogs": 3, "InsertPromptRecords": 1}
	for op, rows := range want {
		if len(got[op]) != 1 || got[op][0] != rows {
			t.Errorf("%s job rows = %v, want [%d]", op, got[op], rows)
		}
	}
}

// The telemetry inserts report their rows as write.rows under their own
// labels, on main's writers and on the shards alike.
func TestIngestInsertsReportRows(t *testing.T) {
	t.Run("unsharded", func(t *testing.T) {
		jobRows := recordJobRows(t)
		repo := setupTestDB(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s := writescheduler.New()
		repo.SetWriteScheduler(s)
		go s.Run(ctx)

		insertTelemetry(t, repo)
		checkIngestRows(t, jobRows())
	})
	t.Run("sharded", func(t *testing.T) {
		jobRows := recordJobRows(t)
		clock := mustTime(t, "2026-10-04T12:00:00Z")
		store := openShardedStorage(t, filepath.Join(t.TempDir(), "spanbarn.db"), &clock)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for _, f := range []Family{FamilyMetrics, FamilyLogs, FamilyPrompts} {
			s := writescheduler.New()
			store.Shards.SetScheduler(f, s)
			go s.Run(ctx)
		}

		insertTelemetry(t, store.Repository())
		checkIngestRows(t, jobRows())
	})
}
