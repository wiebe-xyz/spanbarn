package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
)

// The retention worker's reads must not need a writer connection. On
// 2026-10-19 and 2026-10-20 GetSpansForAggregation and the retained-trace read
// ran on the single writer connection outside the write scheduler, and
// TouchAPIKey and InsertMetrics waited 30s behind them until the watchdog
// reported a wedge (#360). The test holds every writer connection and requires
// the reads of the repository retentionReads builds to finish while they are
// held. The log trim scans a window with nothing old enough to delete, so it
// needs no writer when it reads on the read-only pool.
func TestRetentionReadsLeaveTheWritersFree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store, err := repository.OpenStorage(ctx, path, repository.StorageOptions{CacheMB: 2, AttachCacheMB: 2})
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	repo := store.Repository()
	if err := repo.SetSetting("retention_interesting_hours", "168"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := repo.InsertLogs(ctx, []model.LogRecord{{ProjectID: 1, Body: "recent"}}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}

	ro, err := repository.OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	var rr retention.Repository = retentionReads(repo, ro)

	for _, h := range store.Handles() {
		conn, err := h.Conn(ctx)
		if err != nil {
			t.Fatalf("hold a writer: %v", err)
		}
		t.Cleanup(func() { conn.Close() })
	}

	cutoff := time.Now().UTC()
	reads := []struct {
		name string
		read func() error
	}{
		{"GetSetting", func() error {
			v, err := rr.GetSetting("retention_interesting_hours")
			if err == nil && v != "168" {
				t.Errorf("GetSetting = %q, want the value written through the writer", v)
			}
			return err
		}},
		{"CountSpansOlderThan", func() error { _, err := rr.CountSpansOlderThan(cutoff); return err }},
		{"GetSpansForAggregation", func() error { _, err := rr.GetSpansForAggregation(cutoff, 10); return err }},
		{"ListProjectIDs", func() error { _, err := rr.ListProjectIDs(); return err }},
		{"MetricRollupWatermark", func() error { _, _, err := rr.MetricRollupWatermark(ctx, 300); return err }},
		{"ProjectNonErrorTraceCountCutoff", func() error {
			_, _, err := rr.ProjectNonErrorTraceCountCutoff(ctx, 1, 10)
			return err
		}},
		{"DeleteLogsOlderThan", func() error {
			n, err := rr.DeleteLogsOlderThan(ctx, cutoff.Add(-time.Hour), cutoff.Add(-time.Hour))
			if err == nil && n != 0 {
				t.Errorf("DeleteLogsOlderThan deleted %d rows, want 0", n)
			}
			return err
		}},
	}
	for _, r := range reads {
		done := make(chan error, 1)
		go func() { done <- r.read() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s: %v", r.name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s is still waiting while the writers are held: it reads on a writer connection", r.name)
		}
	}
}
