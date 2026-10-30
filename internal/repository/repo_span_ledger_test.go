package repository

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func spanLedgerRepo(t *testing.T) *Repository {
	t.Helper()
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewRepository(db.DB)
}

func TestAddSpanLedgerRowsAddsToStoredCounts(t *testing.T) {
	repo := spanLedgerRepo(t)
	first := []model.SpanLedgerRow{{
		Bucket: 1_800_000_000, ProjectID: 1, Accepted: 10, SampledOut: 1, ShedUnsampled: 2,
		LostInBuffer: 3, Forwarded: 4, Processed: 5, Failed: 6,
	}}
	if err := repo.AddSpanLedgerRows(first); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSpanLedgerRows(first); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSpanLedgerRows(nil); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListSpanLedger(context.Background(), 1, time.Unix(1_800_000_000, 0), time.Unix(1_800_000_300, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := model.SpanLedgerRow{
		Bucket: 1_800_000_000, ProjectID: 1, Accepted: 20, SampledOut: 2, ShedUnsampled: 4,
		LostInBuffer: 6, Forwarded: 8, Processed: 10, Failed: 12,
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("rows = %+v, want %+v", got, want)
	}
}

func TestListSpanLedgerFilters(t *testing.T) {
	repo := spanLedgerRepo(t)
	if err := repo.AddSpanLedgerRows([]model.SpanLedgerRow{
		{Bucket: 1_800_000_000, ProjectID: 1, Accepted: 1},
		{Bucket: 1_800_000_000, ProjectID: 2, Accepted: 2},
		{Bucket: 1_800_000_300, ProjectID: 1, Accepted: 3},
		{Bucket: 1_800_000_600, ProjectID: 1, Accepted: 4},
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// From inside a bucket includes that bucket; to is exclusive.
	got, err := repo.ListSpanLedger(ctx, 1, time.Unix(1_800_000_100, 0), time.Unix(1_800_000_600, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Accepted != 1 || got[1].Accepted != 3 {
		t.Errorf("project 1 = %+v, want the first two buckets", got)
	}

	all, err := repo.ListSpanLedger(ctx, 0, time.Unix(1_800_000_000, 0), time.Unix(1_800_000_300, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("all projects = %+v, want 2 rows", all)
	}
}

func TestDeleteExpiredSpanLedger(t *testing.T) {
	repo := spanLedgerRepo(t)
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-spanLedgerRetention - time.Hour).Unix()
	future := now.Add(spanLedgerFutureMax + time.Hour).Unix()
	if err := repo.AddSpanLedgerRows([]model.SpanLedgerRow{
		{Bucket: old, ProjectID: 1, Accepted: 1},
		{Bucket: now.Unix(), ProjectID: 1, Accepted: 2},
		{Bucket: future, ProjectID: 1, Accepted: 3},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := repo.DeleteExpiredSpanLedger(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2", n)
	}
	got, err := repo.ListSpanLedger(context.Background(), 1, time.Unix(0, 0), now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Accepted != 2 {
		t.Errorf("rows left = %+v, want the current bucket", got)
	}
}
