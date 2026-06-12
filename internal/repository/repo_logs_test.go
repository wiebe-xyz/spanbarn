package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func makeLogRecord(projectID int64, traceID, body string, severity int32) model.LogRecord {
	attrs, _ := json.Marshal(map[string]string{"service.name": "test-svc"})
	return model.LogRecord{
		ProjectID:            projectID,
		TraceID:              traceID,
		SeverityNumber:       severity,
		SeverityText:         "INFO",
		TimeUnixNano:         uint64(time.Now().UnixNano()),
		ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
		Body:                 body,
		Attributes:           attrs,
	}
}

func TestInsertLogsEmpty(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertLogs(context.Background(), nil); err != nil {
		t.Fatalf("InsertLogs(nil): %v", err)
	}
}

func TestInsertAndQueryLogs(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	recs := []model.LogRecord{
		makeLogRecord(1, "trace-abc", "hello world", 9),
		makeLogRecord(1, "", "no trace log", 13),
		makeLogRecord(2, "trace-xyz", "other project", 17),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	rows, total, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 1,
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Fatalf("want total=2, got %d", total)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}

	// Project 2 isolation.
	rows2, total2, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 2,
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs p2: %v", err)
	}
	if total2 != 1 || len(rows2) != 1 {
		t.Fatalf("want 1 row for project 2, got total=%d rows=%d", total2, len(rows2))
	}
}

func TestQueryLogsTraceFilter(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	recs := []model.LogRecord{
		makeLogRecord(1, "trace-abc", "msg1", 9),
		makeLogRecord(1, "trace-def", "msg2", 9),
		makeLogRecord(1, "", "msg3", 9),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	rows, total, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 1,
		TraceID:   "trace-abc",
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs trace filter: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("want 1, got total=%d rows=%d", total, len(rows))
	}
	if rows[0].Body != "msg1" {
		t.Errorf("want body=msg1, got %q", rows[0].Body)
	}
}

func TestQueryLogsSeverityFilter(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	recs := []model.LogRecord{
		makeLogRecord(1, "", "debug", 5),
		makeLogRecord(1, "", "info", 9),
		makeLogRecord(1, "", "warn", 13),
		makeLogRecord(1, "", "error", 17),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	rows, total, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID:   1,
		MinSeverity: 13,
		From:        now.Add(-time.Minute),
		To:          now.Add(time.Minute),
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("QueryLogs severity filter: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("want 2, got total=%d rows=%d", total, len(rows))
	}
}

func TestQueryLogsBodySearch(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	recs := []model.LogRecord{
		makeLogRecord(1, "", "connection timeout occurred", 9),
		makeLogRecord(1, "", "request processed successfully", 9),
		makeLogRecord(1, "", "connection refused", 17),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	rows, total, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 1,
		Search:    "connection",
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs body search: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("want 2, got total=%d rows=%d", total, len(rows))
	}
}

func TestDeleteLogsOlderThan_Basic(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	errorLogCutoff := now.Add(-30 * 24 * time.Hour)

	if err := repo.InsertLogs(context.Background(), []model.LogRecord{
		makeLogRecord(1, "", "old log", 9),
	}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	// Nothing deleted when cutoff is in the past.
	n, err := repo.DeleteLogsOlderThan(context.Background(), now.Add(-time.Hour), errorLogCutoff)
	if err != nil {
		t.Fatalf("DeleteLogsOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 deleted, got %d", n)
	}

	// Delete with future cutoff removes the log.
	n, err = repo.DeleteLogsOlderThan(context.Background(), now.Add(time.Hour), errorLogCutoff)
	if err != nil {
		t.Fatalf("DeleteLogsOlderThan future: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 deleted, got %d", n)
	}
}

func TestDeleteLogsOlderThan_SkipsPinned(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	errorLogCutoff := now.Add(-30 * 24 * time.Hour)

	recs := []model.LogRecord{
		makeLogRecord(1, "pinned-trace", "pinned log", 9),
		makeLogRecord(1, "regular-trace", "regular log", 9),
		makeLogRecord(1, "", "no-trace log", 9),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	if err := repo.PinTrace(context.Background(), 1, "pinned-trace", "important"); err != nil {
		t.Fatalf("PinTrace: %v", err)
	}

	n, err := repo.DeleteLogsOlderThan(context.Background(), now.Add(time.Hour), errorLogCutoff)
	if err != nil {
		t.Fatalf("DeleteLogsOlderThan: %v", err)
	}
	// regular-trace and no-trace logs should be deleted; pinned-trace log survives.
	if n != 2 {
		t.Errorf("want 2 deleted, got %d", n)
	}

	rows, _, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 1,
		TraceID:   "pinned-trace",
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs after delete: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("want 1 pinned log to survive, got %d", len(rows))
	}
}

func TestDeleteLogsOlderThan_SkipsErrorSampled(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	// errorLogCutoff 30 days ago → error_samples rows inserted now are within the window.
	errorLogCutoff := now.Add(-30 * 24 * time.Hour)

	recs := []model.LogRecord{
		makeLogRecord(1, "error-trace", "error log", 17),
		makeLogRecord(1, "normal-trace", "normal log", 9),
	}
	if err := repo.InsertLogs(context.Background(), recs); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	// Insert an error sample with "error-trace" to protect its logs.
	errSpan := makeSpan(1, "error-trace", "span1", "web", "op", "error", 1000)
	if err := repo.InsertErrorSamples([]Span{errSpan}); err != nil {
		t.Fatalf("InsertErrorSamples: %v", err)
	}

	n, err := repo.DeleteLogsOlderThan(context.Background(), now.Add(time.Hour), errorLogCutoff)
	if err != nil {
		t.Fatalf("DeleteLogsOlderThan: %v", err)
	}
	// normal-trace log deleted; error-trace log survives.
	if n != 1 {
		t.Errorf("want 1 deleted, got %d", n)
	}

	rows, _, err := repo.QueryLogs(context.Background(), LogFilter{
		ProjectID: 1,
		TraceID:   "error-trace",
		From:      now.Add(-time.Minute),
		To:        now.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryLogs after delete: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("want 1 error-sampled log to survive, got %d", len(rows))
	}
}

func TestPinnedTracesCRUD(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()

	// Pin a trace.
	if err := repo.PinTrace(ctx, 1, "trace-abc", "checkout bug"); err != nil {
		t.Fatalf("PinTrace: %v", err)
	}

	// IsTracePinned should return true.
	pinned, err := repo.IsTracePinned(ctx, 1, "trace-abc")
	if err != nil {
		t.Fatalf("IsTracePinned: %v", err)
	}
	if !pinned {
		t.Error("expected trace to be pinned")
	}

	// Different project: not pinned.
	pinned2, err := repo.IsTracePinned(ctx, 2, "trace-abc")
	if err != nil {
		t.Fatalf("IsTracePinned p2: %v", err)
	}
	if pinned2 {
		t.Error("trace should not be pinned for project 2")
	}

	// List.
	list, err := repo.ListPinnedTraces(ctx, 1)
	if err != nil {
		t.Fatalf("ListPinnedTraces: %v", err)
	}
	if len(list) != 1 || list[0].TraceID != "trace-abc" || list[0].Label != "checkout bug" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// Update label via upsert.
	if err := repo.PinTrace(ctx, 1, "trace-abc", "updated label"); err != nil {
		t.Fatalf("PinTrace upsert: %v", err)
	}
	list2, _ := repo.ListPinnedTraces(ctx, 1)
	if len(list2) != 1 || list2[0].Label != "updated label" {
		t.Fatalf("expected label update, got %+v", list2)
	}

	// Unpin.
	if err := repo.UnpinTrace(ctx, 1, "trace-abc"); err != nil {
		t.Fatalf("UnpinTrace: %v", err)
	}

	pinned3, err := repo.IsTracePinned(ctx, 1, "trace-abc")
	if err != nil {
		t.Fatalf("IsTracePinned after unpin: %v", err)
	}
	if pinned3 {
		t.Error("trace should not be pinned after unpin")
	}

	list3, _ := repo.ListPinnedTraces(ctx, 1)
	if len(list3) != 0 {
		t.Errorf("expected empty list after unpin, got %d", len(list3))
	}
}
