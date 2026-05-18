package repository

import (
	"testing"
	"time"
)

func TestSavedQueriesCRUD(t *testing.T) {
	repo := setupTestDB(t)

	project, err := repo.CreateProject("svc", "Saved Queries Test")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	id, err := repo.CreateSavedQuery(SavedQuery{
		ProjectID:     project.ID,
		Name:          "slow checkouts",
		Service:       "checkout-api",
		Operation:     "POST /orders",
		Status:        "ERROR",
		MinDurationUs: 500_000,
	})
	if err != nil {
		t.Fatalf("CreateSavedQuery: %v", err)
	}
	if id == 0 {
		t.Fatalf("CreateSavedQuery returned id 0")
	}

	list, err := repo.ListSavedQueries(project.ID)
	if err != nil {
		t.Fatalf("ListSavedQueries: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListSavedQueries len = %d, want 1", len(list))
	}
	got := list[0]
	if got.ID != id || got.Name != "slow checkouts" || got.Service != "checkout-api" {
		t.Fatalf("unexpected saved query: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt not populated")
	}

	// Listing by an unknown project is empty, not an error.
	empty, err := repo.ListSavedQueries(project.ID + 999)
	if err != nil {
		t.Fatalf("ListSavedQueries unknown project: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(empty))
	}

	if err := repo.DeleteSavedQuery(id); err != nil {
		t.Fatalf("DeleteSavedQuery: %v", err)
	}
	after, err := repo.ListSavedQueries(project.ID)
	if err != nil {
		t.Fatalf("ListSavedQueries post-delete: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected empty after delete, got %d entries", len(after))
	}

	// Deleting an unknown id is a no-op, not an error.
	if err := repo.DeleteSavedQuery(999_999); err != nil {
		t.Fatalf("DeleteSavedQuery unknown id: %v", err)
	}
}

func TestSetQueryTimeout(t *testing.T) {
	repo := setupTestDB(t)

	if repo.queryTimeout != DefaultQueryTimeout {
		t.Fatalf("queryTimeout = %v, want default %v", repo.queryTimeout, DefaultQueryTimeout)
	}

	repo.SetQueryTimeout(2 * time.Second)
	if repo.queryTimeout != 2*time.Second {
		t.Fatalf("after SetQueryTimeout: %v, want 2s", repo.queryTimeout)
	}
}
