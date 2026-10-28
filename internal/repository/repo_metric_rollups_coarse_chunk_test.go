package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func coarseBatch(n int, bucket time.Time) []MetricRollup {
	out := make([]MetricRollup, n)
	for i := range out {
		// Reverse key order, so the chunks only come out sorted if they are sorted.
		out[i] = coarseRow(3600, bucket, 5)
		out[i].AttrFingerprint = fmt.Sprintf("fp%05d", n-i)
	}
	return out
}

// A pass larger than one chunk is written as several chunks in lookup-key order.
func TestCoarseRollupChunksAreBoundedAndSorted(t *testing.T) {
	bucket := time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)
	assertChunksBoundedAndSorted(t, coarseRollupChunks(coarseBatch(2*coarseRollupChunk+7, bucket)), coarseRollupChunk)
}

// A pass stopped after its first chunk and run again gives the rows one clean
// pass gives: the chunks written before the stop are replaced, never added to.
func TestUpsertCoarseRollupsStoppedPassRedoneGivesSameRows(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	db := repo.writer(FamilyCore)
	failKey := fmt.Sprintf("fp%05d", coarseRollupChunk+1) // first key of the second chunk
	if _, err := db.Exec(`CREATE TRIGGER fail_second_chunk BEFORE INSERT ON metric_rollups_coarse
		WHEN NEW.attr_fingerprint = '` + failKey + `' BEGIN SELECT RAISE(ABORT, 'chunk failed'); END`); err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC)
	batch := coarseBatch(2*coarseRollupChunk+7, bucket)
	if err := repo.UpsertCoarseRollups(ctx, batch); err == nil {
		t.Fatal("UpsertCoarseRollups with a failing chunk returned no error")
	}
	var rows int
	var sum float64
	read := func() {
		t.Helper()
		if err := repo.DB().QueryRow(`SELECT count(*), COALESCE(sum(sum), 0) FROM metric_rollups_coarse`).Scan(&rows, &sum); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if rows != coarseRollupChunk {
		t.Fatalf("after the stop: %d rows, want the first chunk's %d", rows, coarseRollupChunk)
	}

	if _, err := db.Exec(`DROP TRIGGER fail_second_chunk`); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertCoarseRollups(ctx, batch); err != nil {
		t.Fatalf("redone pass: %v", err)
	}
	read()
	if rows != len(batch) || sum != float64(5*len(batch)) {
		t.Errorf("after the redo: %d rows with sum %v, want %d rows with sum %d", rows, sum, len(batch), 5*len(batch))
	}
}

// The warm read seeks the row the upsert replaces through the lookup index.
func TestCoarseRollupWarmSeeksLookupIndex(t *testing.T) {
	repo := setupTestDB(t)
	plan := queryPlan(t, repo, coarseRollupWarmSQL, 1, "reqs", "fp", 3600, time.Now().UTC())
	if !strings.Contains(plan, "idx_metric_rollups_coarse_lookup") || strings.Contains(plan, "SCAN") {
		t.Errorf("warm read does not seek idx_metric_rollups_coarse_lookup:\n%s", plan)
	}
}
