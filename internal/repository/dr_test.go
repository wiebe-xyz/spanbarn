package repository

import (
	"context"
	"path/filepath"
	"testing"
)

// seedSettingsAndTelemetry populates every settings table plus one telemetry
// table (spans) so tests can assert the snapshot copies the former and drops
// the latter.
func seedSettingsAndTelemetry(t *testing.T, repo *Repository) (projectID int64) {
	t.Helper()
	ctx := context.Background()

	proj, err := repo.CreateProject("acme", "Acme")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := repo.CreateUser("alice", "hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := repo.CreateAPIKey(proj.ID, "key1", "keyhash", "ingest"); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if _, err := repo.db.Exec(`INSERT INTO alerts (project_id, service, type, threshold) VALUES (?, 'svc', 'error_rate', 0.5)`, proj.ID); err != nil {
		t.Fatalf("insert alert: %v", err)
	}
	if _, err := repo.db.Exec(`INSERT INTO settings (key, value) VALUES ('retention_paused', 'false')`); err != nil {
		t.Fatalf("insert setting: %v", err)
	}
	if _, err := repo.db.Exec(`INSERT INTO saved_queries (project_id, name) VALUES (?, 'slow requests')`, proj.ID); err != nil {
		t.Fatalf("insert saved query: %v", err)
	}
	if _, err := repo.db.Exec(`INSERT INTO trace_exclusions (project_id, operation) VALUES (?, 'GET /health')`, proj.ID); err != nil {
		t.Fatalf("insert trace exclusion: %v", err)
	}
	if err := repo.InsertSpansContext(ctx, []Span{{
		ProjectID: proj.ID, TraceID: "t1", SpanID: "s1", Name: "op", Service: "svc",
		StartTimeUs: 1, DurationUs: 2,
	}}); err != nil {
		t.Fatalf("InsertSpansContext: %v", err)
	}
	return proj.ID
}

func TestSnapshotSettingsCopiesSettingsOnly(t *testing.T) {
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "src.db")

	db, err := NewDB(srcPath)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()
	if err := Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	seedSettingsAndTelemetry(t, repo)

	destPath := filepath.Join(t.TempDir(), "settings.db")
	counts, err := SnapshotSettings(ctx, srcPath, destPath)
	if err != nil {
		t.Fatalf("SnapshotSettings: %v", err)
	}

	wantCounts := map[string]int64{
		"projects":         1,
		"users":            1,
		"api_keys":         1,
		"alerts":           1,
		"settings":         1,
		"saved_queries":    1,
		"trace_exclusions": 1,
	}
	for table, want := range wantCounts {
		if got := counts[table]; got != want {
			t.Errorf("counts[%s] = %d, want %d", table, got, want)
		}
	}

	destDB, err := NewDB(destPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer destDB.Close()

	destRepo := NewRepository(destDB.DB)
	if _, err := destRepo.GetProjectBySlug("acme"); err != nil {
		t.Fatalf("project missing from snapshot: %v", err)
	}
	if _, err := destRepo.GetUserByUsername("alice"); err != nil {
		t.Fatalf("user missing from snapshot: %v", err)
	}

	for _, telemetryTable := range []string{"spans", "aggregates", "error_samples", "metrics", "logs", "prompt_records"} {
		var count int
		if err := destDB.QueryRow("SELECT count(*) FROM " + telemetryTable).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", telemetryTable, err)
		}
		if count != 0 {
			t.Errorf("expected 0 rows in telemetry table %s, got %d", telemetryTable, count)
		}
	}
}

// TestSnapshotSettingsOverwritesExisting verifies a second snapshot to the
// same destPath (as a CronJob would run repeatedly) replaces stale output
// rather than erroring or appending duplicate rows.
func TestSnapshotSettingsOverwritesExisting(t *testing.T) {
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "src.db")

	db, err := NewDB(srcPath)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	defer db.Close()
	if err := Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := NewRepository(db.DB)
	seedSettingsAndTelemetry(t, repo)

	destPath := filepath.Join(t.TempDir(), "settings.db")
	if _, err := SnapshotSettings(ctx, srcPath, destPath); err != nil {
		t.Fatalf("first SnapshotSettings: %v", err)
	}
	if _, err := repo.CreateProject("beta", "Beta"); err != nil {
		t.Fatalf("CreateProject beta: %v", err)
	}
	counts, err := SnapshotSettings(ctx, srcPath, destPath)
	if err != nil {
		t.Fatalf("second SnapshotSettings: %v", err)
	}
	if counts["projects"] != 2 {
		t.Fatalf("expected 2 projects after re-snapshot, got %d", counts["projects"])
	}
}
