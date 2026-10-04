package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func sloFixture(t *testing.T) (*Repository, int64, int64) {
	t.Helper()
	repo := setupTestDB(t)
	p, err := repo.CreateProject("svc", "SLO Test")
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateSLO(SLO{
		ProjectID: p.ID, Name: "availability", Target: 0.995, WindowDays: 30,
		GoodFilter:  []byte(`{"match":"and","filters":[{"key":"status","op":"!=","value":"error"}]}`),
		TotalFilter: []byte(`{"match":"and","filters":[{"key":"kind","op":"=","value":"server"}]}`),
	})
	if err != nil {
		t.Fatalf("CreateSLO: %v", err)
	}
	return repo, p.ID, id
}

func TestSLOCRUDIsProjectScoped(t *testing.T) {
	repo, pid, id := sloFixture(t)
	other, _ := repo.CreateProject("other", "Other")

	got, err := repo.GetSLO(pid, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "availability" || got.Target != 0.995 || got.WindowDays != 30 || len(got.GoodFilter) == 0 || got.CreatedAt.IsZero() {
		t.Fatalf("slo = %+v", got)
	}
	if _, err := repo.GetSLO(other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project get = %v", err)
	}
	if list, _ := repo.ListSLOs(other.ID); len(list) != 0 {
		t.Fatalf("other project sees %d", len(list))
	}

	got.Name, got.Target = "latency", 0.99
	if err := repo.UpdateSLO(*got); err != nil {
		t.Fatal(err)
	}
	got.ProjectID = other.ID
	if err := repo.UpdateSLO(*got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project update = %v", err)
	}
	if err := repo.DeleteSLO(other.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project delete = %v", err)
	}
	if list, _ := repo.ListSLOs(pid); len(list) != 1 || list[0].Name != "latency" {
		t.Fatalf("list = %+v", list)
	}
	if err := repo.DeleteSLO(pid, id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetSLO(pid, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestSLONameConflict(t *testing.T) {
	repo, pid, _ := sloFixture(t)
	_, err := repo.CreateSLO(SLO{ProjectID: pid, Name: "availability", Target: 0.9, WindowDays: 7})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create = %v", err)
	}
	id2, err := repo.CreateSLO(SLO{ProjectID: pid, Name: "second", Target: 0.9, WindowDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.UpdateSLO(SLO{ID: id2, ProjectID: pid, Name: "availability", Target: 0.9, WindowDays: 7})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto existing = %v", err)
	}
	other, _ := repo.CreateProject("other", "Other")
	if _, err := repo.CreateSLO(SLO{ProjectID: other.ID, Name: "availability", Target: 0.9, WindowDays: 7}); err != nil {
		t.Fatalf("same name in other project: %v", err)
	}
}

func TestSLOTargetBoundsRejected(t *testing.T) {
	repo, pid, _ := sloFixture(t)
	for _, target := range []float64{0, 1, 1.5, -0.1} {
		if _, err := repo.CreateSLO(SLO{ProjectID: pid, Name: "bad", Target: target, WindowDays: 7}); err == nil {
			t.Errorf("target %v accepted", target)
		}
	}
}

func TestSLOBurnAlertsScopedAndState(t *testing.T) {
	repo, pid, id := sloFixture(t)
	other, _ := repo.CreateProject("other", "Other")

	a := SLOBurnAlert{SLOID: id, WindowMinutes: 60, BurnRate: 14.4, WebhookURL: "https://x.test/h", Email: "a@x.test", CooldownMinutes: 30, Enabled: true}
	if _, err := repo.CreateSLOBurnAlert(other.ID, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project create = %v", err)
	}
	aid, err := repo.CreateSLOBurnAlert(pid, a)
	if err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListSLOBurnAlerts(pid, id)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got := list[0]
	if got.ID != aid || got.BurnRate != 14.4 || got.WebhookURL != "https://x.test/h" || !got.Enabled || got.Firing || got.LastTriggeredAt.Valid {
		t.Fatalf("alert = %+v", got)
	}
	if l, _ := repo.ListSLOBurnAlerts(other.ID, id); len(l) != 0 {
		t.Fatal("other project lists alerts")
	}

	got.BurnRate, got.Enabled = 6, false
	if err := repo.UpdateSLOBurnAlert(other.ID, got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project update = %v", err)
	}
	if err := repo.UpdateSLOBurnAlert(pid, got); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := repo.ListEnabledSLOBurnAlerts(); len(enabled) != 0 {
		t.Fatalf("disabled alert listed: %+v", enabled)
	}

	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.UpdateSLOBurnAlertState(aid, true, at); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetSLOBurnAlert(pid, aid)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Firing || !after.LastTriggeredAt.Valid || !after.LastTriggeredAt.Time.Equal(at) || after.BurnRate != 6 {
		t.Fatalf("after state = %+v", after)
	}
	if err := repo.UpdateSLOBurnAlertState(aid, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	after, _ = repo.GetSLOBurnAlert(pid, aid)
	if after.Firing || !after.LastTriggeredAt.Time.Equal(at) {
		t.Fatalf("recovery lost trigger time: %+v", after)
	}

	if err := repo.DeleteSLOBurnAlert(other.ID, aid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project delete = %v", err)
	}
	if err := repo.DeleteSLOBurnAlert(pid, aid); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSLORemovesAlertsAndCounts(t *testing.T) {
	repo, pid, id := sloFixture(t)
	if _, err := repo.CreateSLOBurnAlert(pid, SLOBurnAlert{SLOID: id, WindowMinutes: 5, BurnRate: 2, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	b := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.InsertSLOCounts([]SLOCount{{SLOID: id, BucketStart: b, Good: 9, Total: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSLO(pid, id); err != nil {
		t.Fatal(err)
	}
	var alerts, counts int
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM slo_burn_alerts`).Scan(&alerts)
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM slo_counts`).Scan(&counts)
	if alerts != 0 || counts != 0 {
		t.Fatalf("left behind: %d alerts, %d counts", alerts, counts)
	}
}

func TestSLOCountsIdempotentSumLatestPrune(t *testing.T) {
	repo, _, id := sloFixture(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	step := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }

	if latest, err := repo.LatestSLOBucket(id); err != nil || !latest.IsZero() {
		t.Fatalf("empty latest = %v, %v", latest, err)
	}
	counts := []SLOCount{
		{SLOID: id, BucketStart: step(0), Good: 8, Total: 10},
		{SLOID: id, BucketStart: step(1), Good: 18, Total: 20},
		{SLOID: id, BucketStart: step(2), Good: 5, Total: 5},
	}
	if err := repo.InsertSLOCounts(counts); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSLOCounts(counts); err != nil {
		t.Fatalf("second insert: %v", err)
	}
	// Re-recording a bucket replaces it.
	if err := repo.InsertSLOCounts([]SLOCount{{SLOID: id, BucketStart: step(1), Good: 19, Total: 21}}); err != nil {
		t.Fatal(err)
	}

	good, total, err := repo.SumSLOCounts(id, step(0), step(3))
	if err != nil || good != 32 || total != 36 {
		t.Fatalf("sum all = %d/%d, %v", good, total, err)
	}
	good, total, _ = repo.SumSLOCounts(id, step(1), step(2))
	if good != 19 || total != 21 {
		t.Fatalf("half-open range = %d/%d", good, total)
	}
	if good, total, _ = repo.SumSLOCounts(id, step(10), step(20)); good != 0 || total != 0 {
		t.Fatalf("empty range = %d/%d", good, total)
	}
	if latest, _ := repo.LatestSLOBucket(id); !latest.Equal(step(2)) {
		t.Fatalf("latest = %v", latest)
	}

	n, err := repo.DeleteSLOCountsBefore(step(2))
	if err != nil || n != 2 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	if good, total, _ = repo.SumSLOCounts(id, step(0), step(3)); good != 5 || total != 5 {
		t.Fatalf("after prune = %d/%d", good, total)
	}
}

func sloTableCount(t *testing.T, repo *Repository) int {
	t.Helper()
	var n int
	err := repo.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('slos', 'slo_burn_alerts', 'slo_counts')`,
	).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSLOMigrationDownReverses(t *testing.T) {
	repo := setupTestDB(t)
	if n := sloTableCount(t, repo); n != 3 {
		t.Fatalf("tables after up = %d", n)
	}
	if err := goose.DownTo(repo.db, ".", 38); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := sloTableCount(t, repo); n != 0 {
		t.Fatalf("tables after down = %d", n)
	}
	if err := Migrate(repo.db); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := sloTableCount(t, repo); n != 3 {
		t.Fatalf("tables after re-up = %d", n)
	}
}
