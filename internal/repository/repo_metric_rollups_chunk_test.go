package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func rollupBatch(n int, bucket time.Time) []MetricRollup {
	out := make([]MetricRollup, n)
	for i := range out {
		// Reverse key order, so the chunks only come out sorted if they are sorted.
		out[i] = MetricRollup{ProjectID: 1, Name: "m", Type: "sum", AttrFingerprint: fmt.Sprintf("fp%05d", n-i),
			Attributes: "{}", Bucket: bucket, Count: 1, Sum: 1, Min: 1, Max: 1, Last: 1, ObsCount: 1}
	}
	return out
}

// A flush larger than one chunk is written as several chunks in key order.
func TestMetricRollupChunksAreBoundedAndSorted(t *testing.T) {
	bucket := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	chunks := metricRollupChunks(rollupBatch(2*metricRollupChunk+7, bucket))
	if len(chunks) != 3 || len(chunks[0]) != metricRollupChunk || len(chunks[1]) != metricRollupChunk || len(chunks[2]) != 7 {
		t.Fatalf("chunk sizes %d/%d/%d of %d chunks", len(chunks[0]), len(chunks[1]), len(chunks[len(chunks)-1]), len(chunks))
	}
	prev := ""
	for _, c := range chunks {
		for _, m := range c {
			if m.AttrFingerprint <= prev {
				t.Fatalf("%s after %s: chunks not in key order", m.AttrFingerprint, prev)
			}
			prev = m.AttrFingerprint
		}
	}
}

// Every row of a multi-chunk flush lands, and a second flush of the same keys
// folds into the rows.
func TestUpsertMetricRollupsWritesEveryChunk(t *testing.T) {
	repo := setupTestDB(t)
	bucket := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	batch := rollupBatch(2*metricRollupChunk+7, bucket)
	for range 2 {
		if err := repo.UpsertMetricRollups(batch); err != nil {
			t.Fatal(err)
		}
	}
	var rows, sum int
	if err := repo.DB().QueryRow(`SELECT count(*), sum(count) FROM metric_rollups`).Scan(&rows, &sum); err != nil {
		t.Fatal(err)
	}
	if rows != len(batch) || sum != 2*len(batch) {
		t.Fatalf("%d rows with count %d, want %d rows with count %d", rows, sum, len(batch), 2*len(batch))
	}
}

// With a read pool the flush reads its keys there before it asks for the
// writer. The test holds the writer's only connection: by the time the upsert
// waits for it, the read pool has opened a connection.
func TestUpsertMetricRollupsWarmsOnTheReadPoolFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	store := openTestStorage(t, path)
	ctx := context.Background()
	repo := store.Repository()
	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	repo.SetRollupReads(ro.DB)
	bucket := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if err := repo.UpsertMetricRollups(rollupBatch(10, bucket)); err != nil {
		t.Fatal(err)
	}
	ro.DB.SetMaxIdleConns(0) // every warm read opens a fresh connection
	opened := ro.DB.Stats().MaxIdleClosed

	writer := repo.writer(FamilyCore)
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waits := writer.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- repo.UpsertMetricRollups(rollupBatch(10, bucket)) }()

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for writer.Stats().WaitCount == waits {
		select {
		case err := <-done:
			t.Fatalf("upsert returned (%v) while the writer was held", err)
		case <-deadline:
			t.Fatal("upsert never asked for the writer")
		case <-tick.C:
		}
	}
	if got := ro.DB.Stats().MaxIdleClosed; got == opened {
		t.Error("the upsert asked for the writer before it read on the read pool")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
