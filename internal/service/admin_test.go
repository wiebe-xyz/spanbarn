package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func adminStore(t *testing.T) (Store, int64) {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := repository.NewRepository(db.DB)
	p, err := repo.CreateProject("admin", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	return repo, p.ID
}

func TestAlertServiceRoundTrip(t *testing.T) {
	store, pid := adminStore(t)
	svc := NewAlertService(store)

	id, err := svc.Create(Alert{ProjectID: pid, Type: "error_rate", Threshold: 5, ComparisonWindow: 5, CooldownMinutes: 10, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	list, err := svc.List(pid)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	a := list[0]
	a.Threshold = 9
	if err := svc.Update(a); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := svc.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if list, _ = svc.List(pid); len(list) != 0 {
		t.Fatalf("alert not deleted: %v", list)
	}
}

func TestSavedQueryAndExclusionServices(t *testing.T) {
	store, pid := adminStore(t)

	sq := NewSavedQueryService(store)
	id, err := sq.Create(SavedQuery{ProjectID: pid, Name: "slow"})
	if err != nil {
		t.Fatalf("Create saved query: %v", err)
	}
	if qs, err := sq.List(pid); err != nil || len(qs) != 1 {
		t.Fatalf("List saved queries = %v, %v", qs, err)
	}
	if err := sq.Delete(id); err != nil {
		t.Fatalf("Delete saved query: %v", err)
	}

	te := NewTraceExclusionService(store)
	eid, err := te.Create(pid, "GET /health")
	if err != nil {
		t.Fatalf("Create exclusion: %v", err)
	}
	if ex, err := te.List(pid); err != nil || len(ex) != 1 {
		t.Fatalf("List exclusions = %v, %v", ex, err)
	}
	if err := te.Delete(eid); err != nil {
		t.Fatalf("Delete exclusion: %v", err)
	}
}

func TestSettingsAndProjectServices(t *testing.T) {
	store, pid := adminStore(t)

	st := NewSettingsService(store)
	if err := st.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if all, err := st.All(); err != nil || all["k"] != "v" {
		t.Fatalf("All = %v, %v", all, err)
	}
	if err := st.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.DBSize(":memory:", t.TempDir()); err != nil {
		t.Logf("DBSize on in-memory db: %v", err)
	}
	if c, err := st.DBCounts(); err != nil || c == nil {
		t.Fatalf("DBCounts = %v, %v", c, err)
	}

	ps := NewProjectService(store)
	if list, err := ps.List(); err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if p, err := ps.ByID(pid); err != nil || p.Slug != "admin" {
		t.Fatalf("ByID = %v, %v", p, err)
	}
	if p, err := ps.BySlug("admin"); err != nil || p.ID != pid {
		t.Fatalf("BySlug = %v, %v", p, err)
	}
	pending, err := ps.EnsurePending("new", "new")
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if err := ps.EnsureSetupAPIKey(pending.ID, "sha"); err != nil {
		t.Fatalf("EnsureSetupAPIKey: %v", err)
	}
	if keys, err := ps.APIKeys(pending.ID); err != nil || len(keys) != 1 {
		t.Fatalf("APIKeys = %v, %v", keys, err)
	}
	if _, err := ps.Approve(pending.ID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := ps.SetE2E(pid, true); err != nil {
		t.Fatalf("SetE2E: %v", err)
	}
	if stats, err := ps.UsageStats(24); err != nil || stats == nil {
		t.Fatalf("UsageStats = %v, %v (must be non-nil)", stats, err)
	}
	if _, err := ps.UpsertE2EUser("e2e:admin", time.Now().Add(E2EAccountTTL)); err != nil {
		t.Fatalf("UpsertE2EUser: %v", err)
	}
	if err := ps.Delete(pending.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestLogsMetricsAndExportServices(t *testing.T) {
	store, pid := adminStore(t)
	ctx := context.Background()
	from, to := time.Now().Add(-time.Hour), time.Now()

	ls := NewLogsService(store)
	if err := ls.Pin(ctx, pid, "trace1", "label"); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if pins, err := ls.ListPinned(ctx, pid); err != nil || len(pins) != 1 {
		t.Fatalf("ListPinned = %v, %v", pins, err)
	}
	if err := ls.Unpin(ctx, pid, "trace1"); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	f := LogFilter{ProjectID: pid, From: from, To: to, Limit: 10}
	if _, _, err := ls.Query(ctx, f); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if _, err := ls.Histogram(ctx, f, 60); err != nil {
		t.Fatalf("Histogram: %v", err)
	}

	ms := NewMetricsService(store)
	if _, err := ms.Names(ctx, pid, from, to); err != nil {
		t.Fatalf("Names: %v", err)
	}
	if _, err := ms.Catalog(ctx, pid, from, to); err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if _, err := ms.Series(ctx, MetricFilter{ProjectID: pid, Name: "m", From: from, To: to, Limit: 10}); err != nil {
		t.Fatalf("Series: %v", err)
	}
	if _, err := ms.Rollups(ctx, MetricRollupFilter{ProjectID: pid, From: from, To: to}); err != nil {
		t.Fatalf("Rollups: %v", err)
	}
	if _, err := ms.CoarseRollups(ctx, CoarseRollupFilter{ProjectID: pid, StepSeconds: 3600, From: from, To: to}); err != nil {
		t.Fatalf("CoarseRollups: %v", err)
	}
	if _, err := ms.ProjectRollups(ctx, pid, from, to, 0); err != nil {
		t.Fatalf("ProjectRollups: %v", err)
	}

	n := 0
	ex := NewExportService(store)
	if err := ex.StreamSpans(SpanFilter{ProjectID: pid, From: from, To: to}, func(Span) error { n++; return nil }); err != nil {
		t.Fatalf("StreamSpans: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no spans, got %d", n)
	}

	if !ValidLabelKey("service.name") || ValidLabelKey("bad key!") {
		t.Fatal("ValidLabelKey wrapper disagrees with the repository")
	}
	_ = MarshalMetricExtra(MetricRow{Name: "m"})
}
