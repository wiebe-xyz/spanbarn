package repository

import (
	"context"
	"testing"
	"time"
)

func TestUpsertAndQueryMetricRollups(t *testing.T) {
	repo := setupTestDB(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	rollups := []MetricRollup{
		{ProjectID: 1, Name: "g", Type: "gauge", Unit: "ms", AttrFingerprint: "fp1", Attributes: `{"svc":"a"}`, Bucket: base, Count: 2, Sum: 30, Min: 10, Max: 20, Last: 20},
		{ProjectID: 1, Name: "g", Type: "gauge", Unit: "ms", AttrFingerprint: "fp1", Attributes: `{"svc":"a"}`, Bucket: base.Add(time.Minute), Count: 1, Sum: 40, Min: 40, Max: 40, Last: 40},
	}
	if err := repo.UpsertMetricRollups(rollups); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.QueryMetricRollups(context.Background(), MetricRollupFilter{
		ProjectID: 1, Name: "g", From: base.Add(-time.Hour), To: base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rollups, got %d", len(got))
	}
	// Ordered by bucket ascending.
	if !got[0].Bucket.Before(got[1].Bucket) {
		t.Errorf("rollups not ordered ascending: %v then %v", got[0].Bucket, got[1].Bucket)
	}
	if got[0].Sum != 30 || got[0].Max != 20 {
		t.Errorf("first rollup wrong: %+v", got[0])
	}
}

func TestUpsertMetricRollupsConflictMerge(t *testing.T) {
	repo := setupTestDB(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	r := MetricRollup{ProjectID: 1, Name: "c", Type: "sum", AttrFingerprint: "fp", Attributes: "{}", Bucket: base, Count: 1, Sum: 10, Min: 10, Max: 10, Last: 10, ObsCount: 1}
	if err := repo.UpsertMetricRollups([]MetricRollup{r}); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	// Same key again (a late straggler): counts/sum/obs add, max takes the larger, last replaced.
	r2 := r
	r2.Count = 2
	r2.Sum = 25
	r2.Max = 30
	r2.Last = 30
	r2.ObsCount = 2
	if err := repo.UpsertMetricRollups([]MetricRollup{r2}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}

	got, err := repo.QueryMetricRollups(context.Background(), MetricRollupFilter{ProjectID: 1, Name: "c", From: base.Add(-time.Hour), To: base.Add(time.Hour)})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 merged rollup, got %d", len(got))
	}
	m := got[0]
	if m.Count != 3 || m.Sum != 35 || m.Max != 30 || m.Last != 30 || m.ObsCount != 3 {
		t.Errorf("conflict merge wrong: %+v", m)
	}
}

func TestQueryMetricRollupsLabelFilter(t *testing.T) {
	repo := setupTestDB(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	if err := repo.UpsertMetricRollups([]MetricRollup{
		{ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "a", Attributes: `{"svc":"a"}`, Bucket: base, Count: 1, Last: 1},
		{ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "b", Attributes: `{"svc":"b"}`, Bucket: base, Count: 1, Last: 2},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.QueryMetricRollups(context.Background(), MetricRollupFilter{
		ProjectID: 1, Name: "g", From: base.Add(-time.Hour), To: base.Add(time.Hour),
		Attributes: map[string]string{"svc": "b"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].Last != 2 {
		t.Fatalf("label filter wrong: %+v", got)
	}
}

func TestDeleteMetricRollupsOlderThan(t *testing.T) {
	repo := setupTestDB(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	if err := repo.UpsertMetricRollups([]MetricRollup{
		{ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "a", Attributes: "{}", Bucket: base.Add(-48 * time.Hour), Count: 1},
		{ProjectID: 1, Name: "g", Type: "gauge", AttrFingerprint: "b", Attributes: "{}", Bucket: base, Count: 1},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	deleted, err := repo.DeleteMetricRollupsOlderThan(context.Background(), base.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 {
		t.Errorf("want 1 deleted, got %d", deleted)
	}
	got, _ := repo.QueryMetricRollups(context.Background(), MetricRollupFilter{ProjectID: 1, Name: "g", From: base.Add(-72 * time.Hour), To: base.Add(time.Hour)})
	if len(got) != 1 {
		t.Errorf("want 1 remaining, got %d", len(got))
	}
}
