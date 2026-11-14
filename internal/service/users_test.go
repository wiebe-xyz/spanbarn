package service

import "testing"

func TestUserServiceRoundTrip(t *testing.T) {
	store, _ := adminStore(t)
	repo, ok := store.(UserRepository)
	if !ok {
		t.Fatal("store does not implement UserRepository")
	}
	svc := NewUserService(repo)

	if err := svc.Create("ada", "hash"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Create("ada", "hash"); err == nil {
		t.Fatal("duplicate Create succeeded")
	}
	users, err := svc.List()
	if err != nil || len(users) != 1 || users[0].Username != "ada" {
		t.Fatalf("List = %v, %v", users, err)
	}
	if err := svc.Delete("ada"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete("ada"); err == nil {
		t.Fatal("Delete of a missing user succeeded")
	}
}

func TestProjectServiceCLIOperations(t *testing.T) {
	store, pid := adminStore(t)
	svc := NewProjectService(store)

	p, err := svc.Create("cli", "CLI")
	if err != nil || p.Slug != "cli" {
		t.Fatalf("Create = %v, %v", p, err)
	}
	// The CLI mints full-scope keys and a reserved name, which the API path refuses.
	id1, err := svc.IssueAPIKey(pid, "ops", "h1", "full")
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if _, err := svc.IssueAPIKey(p.ID, "ingest", "h2", "ingest"); err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	all, err := svc.AllAPIKeys()
	if err != nil || len(all) != 2 {
		t.Fatalf("AllAPIKeys = %v, %v", all, err)
	}
	if err := svc.RevokeKey(id1); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if all, _ = svc.AllAPIKeys(); len(all) != 1 {
		t.Fatalf("keys after revoke = %v", all)
	}
}
