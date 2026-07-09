package repository

import (
	"testing"
	"time"
)

func TestUpsertE2EUserAndExpiry(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	// Create an E2E user; it should be retrievable and carry no usable password.
	u, err := repo.UpsertE2EUser("e2e-alice", now.Add(E2EAccountTTL))
	if err != nil {
		t.Fatalf("UpsertE2EUser: %v", err)
	}
	if u.Username != "e2e-alice" {
		t.Errorf("username = %q, want e2e-alice", u.Username)
	}
	if u.PasswordHash != "" {
		t.Errorf("E2E user password hash should be empty, got %q", u.PasswordHash)
	}

	// Upsert again with a new expiry — must not create a duplicate.
	if _, err := repo.UpsertE2EUser("e2e-alice", now.Add(2*E2EAccountTTL)); err != nil {
		t.Fatalf("UpsertE2EUser (refresh): %v", err)
	}

	// A second, already-expired E2E user.
	if _, err := repo.UpsertE2EUser("e2e-bob", now.Add(-time.Hour)); err != nil {
		t.Fatalf("UpsertE2EUser (expired): %v", err)
	}

	// Only the expired one is swept.
	n, err := repo.DeleteExpiredE2EUsers(now)
	if err != nil {
		t.Fatalf("DeleteExpiredE2EUsers: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d expired E2E users, want 1", n)
	}
	if _, err := repo.GetUserByUsername("e2e-alice"); err != nil {
		t.Errorf("non-expired e2e-alice should remain: %v", err)
	}
	if _, err := repo.GetUserByUsername("e2e-bob"); err == nil {
		t.Error("expired e2e-bob should have been deleted")
	}
}
