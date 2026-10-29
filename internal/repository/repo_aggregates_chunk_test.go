package repository

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// A batch over aggregateChunk rows is written in jobs of at most
// aggregateChunk rows each, and every row lands. Writing the batch again adds
// into the same rows.
func TestUpsertAggregatesWritesInChunks(t *testing.T) {
	jobRows := recordJobRows(t)
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)

	bucket := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	var aggs []Aggregate
	for i := range aggregateChunk*2 + 7 {
		// Reverse order, so the write has to sort the batch.
		aggs = append(aggs, Aggregate{
			ProjectID: 1, Service: "svc", Operation: fmt.Sprintf("op-%04d", aggregateChunk*3-i),
			Kind: "server", Bucket: bucket, Count: 1, SumDurationUs: 10,
		})
	}
	for range 2 {
		if err := repo.UpsertAggregates(aggs); err != nil {
			t.Fatalf("UpsertAggregates: %v", err)
		}
	}

	got := jobRows()["UpsertAggregates"]
	want := []int64{aggregateChunk, aggregateChunk, 7, aggregateChunk, aggregateChunk, 7}
	if !slices.Equal(got, want) {
		t.Errorf("job rows = %v, want %v", got, want)
	}
	var n, minCount, maxCount, sum int64
	if err := repo.DB().QueryRow(`SELECT COUNT(*), MIN(count), MAX(count), SUM(sum_duration_us) FROM aggregates`).
		Scan(&n, &minCount, &maxCount, &sum); err != nil {
		t.Fatalf("read aggregates: %v", err)
	}
	if n != int64(len(aggs)) || minCount != 2 || maxCount != 2 || sum != 20*n {
		t.Errorf("rows %d count %d..%d sum %d, want %d rows of count 2 and sum 20", n, minCount, maxCount, sum, len(aggs))
	}
}

func TestCompareAggregateKeyFollowsTheLookupIndex(t *testing.T) {
	b := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	rows := []Aggregate{
		{ProjectID: 2, Service: "a", Operation: "a", Kind: "a", Bucket: b},
		{ProjectID: 1, Service: "b", Operation: "a", Kind: "a", Bucket: b},
		{ProjectID: 1, Service: "a", Operation: "b", Kind: "a", Bucket: b},
		{ProjectID: 1, Service: "a", Operation: "a", Resource: "r", Kind: "a", Bucket: b},
		{ProjectID: 1, Service: "a", Operation: "a", Kind: "b", Bucket: b},
		{ProjectID: 1, Service: "a", Operation: "a", Kind: "a", Bucket: b.Add(time.Minute)},
		{ProjectID: 1, Service: "a", Operation: "a", Kind: "a", Bucket: b},
	}
	sorted := slices.Clone(rows)
	slices.SortFunc(sorted, compareAggregateKey)
	slices.Reverse(rows)
	for i := range rows {
		if compareAggregateKey(sorted[i], rows[i]) != 0 {
			t.Fatalf("position %d = %+v, want %+v", i, sorted[i], rows[i])
		}
	}
}
