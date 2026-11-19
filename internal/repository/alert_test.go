package repository

import (
	"testing"
	"time"
)

func TestCreateAndListAlerts(t *testing.T) {
	repo := setupTestDB(t)
	p, err := repo.CreateProject("proj", "Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	alert := Alert{
		ProjectID:        p.ID,
		Service:          "web",
		Operation:        "GET /api",
		Type:             "latency",
		Threshold:        100.0,
		ComparisonWindow: 10,
		CooldownMinutes:  30,
		WebhookURL:       "https://hooks.example.com/alert",
		Email:            "ops@example.com",
		Enabled:          true,
	}

	id, err := repo.CreateAlert(alert)
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	alerts, err := repo.ListAlerts(p.ID)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}

	a := alerts[0]
	if a.ID != id {
		t.Fatalf("expected ID=%d, got %d", id, a.ID)
	}
	if a.Service != "web" {
		t.Fatalf("expected service=web, got %s", a.Service)
	}
	if a.Type != "latency" {
		t.Fatalf("expected type=latency, got %s", a.Type)
	}
	if a.Threshold != 100.0 {
		t.Fatalf("expected threshold=100.0, got %f", a.Threshold)
	}
	if !a.Enabled {
		t.Fatal("expected enabled=true")
	}
	if a.WebhookURL != "https://hooks.example.com/alert" {
		t.Fatalf("expected webhook URL, got %s", a.WebhookURL)
	}
	if a.Email != "ops@example.com" {
		t.Fatalf("expected email, got %s", a.Email)
	}

	// Create a second alert for same project.
	alert2 := Alert{
		ProjectID:        p.ID,
		Service:          "worker",
		Type:             "error_rate",
		Threshold:        5.0,
		ComparisonWindow: 5,
		CooldownMinutes:  15,
		Enabled:          true,
	}
	_, err = repo.CreateAlert(alert2)
	if err != nil {
		t.Fatalf("CreateAlert second: %v", err)
	}

	alerts, err = repo.ListAlerts(p.ID)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(alerts))
	}
}

func TestUpdateAlertLastTriggered(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Project")

	id, _ := repo.CreateAlert(Alert{
		ProjectID:        p.ID,
		Service:          "web",
		Type:             "latency",
		Threshold:        100.0,
		ComparisonWindow: 10,
		CooldownMinutes:  30,
		Enabled:          true,
	})

	// Initially not triggered.
	alerts, _ := repo.ListAlerts(p.ID)
	if alerts[0].LastTriggeredAt.Valid {
		t.Fatal("expected last_triggered_at to be NULL initially")
	}

	// Trigger.
	triggerTime := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	if err := repo.UpdateAlertLastTriggered(id, triggerTime); err != nil {
		t.Fatalf("UpdateAlertLastTriggered: %v", err)
	}

	alerts, _ = repo.ListAlerts(p.ID)
	if !alerts[0].LastTriggeredAt.Valid {
		t.Fatal("expected last_triggered_at to be set")
	}
	// Check the time is approximately correct (SQLite may lose sub-second precision).
	diff := alerts[0].LastTriggeredAt.Time.Sub(triggerTime)
	if diff < -time.Second || diff > time.Second {
		t.Fatalf("trigger time mismatch: got %v, want %v", alerts[0].LastTriggeredAt.Time, triggerTime)
	}
}

func TestDeleteAlert(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Project")

	id, _ := repo.CreateAlert(Alert{
		ProjectID:        p.ID,
		Service:          "web",
		Type:             "latency",
		Threshold:        100.0,
		ComparisonWindow: 10,
		CooldownMinutes:  30,
		Enabled:          true,
	})

	if err := repo.DeleteAlert(id); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}

	alerts, _ := repo.ListAlerts(p.ID)
	if len(alerts) != 0 {
		t.Fatalf("expected 0 alerts after delete, got %d", len(alerts))
	}

	// Delete nonexistent.
	if err := repo.DeleteAlert(9999); err == nil {
		t.Fatal("expected error for nonexistent alert")
	}
}

func TestAlertEnvironmentRoundTrips(t *testing.T) {
	repo := setupTestDB(t)
	p, err := repo.CreateProject("proj", "Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	id, err := repo.CreateAlert(Alert{ProjectID: p.ID, Environment: "production", Service: "web", Type: "error_rate", Threshold: 5, Enabled: true})
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}
	got, err := repo.ListAlerts(p.ID)
	if err != nil || len(got) != 1 || got[0].Environment != "production" {
		t.Fatalf("created alert reads %+v, %v; want environment production", got, err)
	}
	got[0].Environment = ""
	if err := repo.UpdateAlert(got[0]); err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}
	got, err = repo.ListAlerts(p.ID)
	if err != nil || len(got) != 1 || got[0].ID != id || got[0].Environment != "" {
		t.Fatalf("updated alert reads %+v, %v; want empty environment", got, err)
	}
}

func TestProjectOrganizationsRoundTrip(t *testing.T) {
	repo := setupTestDB(t)
	p, err := repo.CreateProject("proj", "Project")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.OrganizationIDs != "" {
		t.Fatalf("new project lists %q, want owner-only", p.OrganizationIDs)
	}
	if err := repo.SetProjectOrganizations(p.ID, "org-a,org-b"); err != nil {
		t.Fatalf("SetProjectOrganizations: %v", err)
	}
	got, err := repo.GetProjectByID(p.ID)
	if err != nil || got.OrganizationIDs != "org-a,org-b" {
		t.Fatalf("project reads %q, %v; want org-a,org-b", got.OrganizationIDs, err)
	}
	if err := repo.SetProjectOrganizations(999, "x"); err == nil {
		t.Fatal("setting an unknown project succeeded")
	}
}
