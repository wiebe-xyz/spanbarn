package service

import (
	"errors"
	"strings"
	"testing"
)

// CreateAPIKey stores ingest and read keys under a trimmed name, and refuses
// the requests the API does not mint without writing anything.
func TestCreateAPIKeyValidates(t *testing.T) {
	store, pid := adminStore(t)
	ps := NewProjectService(store)

	id, err := ps.CreateAPIKey(pid, " ingest-2026-10 ", "ingest", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.CreateAPIKey(pid, "reader", "read", "hash-2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, scope string }{
		{"x", "full"},
		{"x", ""},
		{"setup", "ingest"},
		{"   ", "ingest"},
		{strings.Repeat("n", maxAPIKeyName+1), "ingest"},
	} {
		if _, err := ps.CreateAPIKey(pid, tc.name, tc.scope, "hash-x"); !errors.Is(err, ErrInvalidAPIKey) {
			t.Errorf("CreateAPIKey(%q, %q) = %v, want ErrInvalidAPIKey", tc.name, tc.scope, err)
		}
	}
	if _, err := ps.CreateAPIKey(pid+100, "x", "ingest", "hash-3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}

	keys, err := ps.APIKeys(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0].ID != id && keys[1].ID != id {
		t.Fatalf("keys = %+v, want the two created", keys)
	}
	for _, k := range keys {
		if k.ID == id && k.Name != "ingest-2026-10" {
			t.Errorf("name stored as %q", k.Name)
		}
	}
}

// RevokeAPIKey removes a key of the given project and reports a key of any
// other project as not found.
func TestRevokeAPIKeyStaysInsideTheProject(t *testing.T) {
	store, pid := adminStore(t)
	ps := NewProjectService(store)
	other, err := ps.EnsurePending("other", "other")
	if err != nil {
		t.Fatal(err)
	}
	mine, err := ps.CreateAPIKey(pid, "mine", "ingest", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := ps.CreateAPIKey(other.ID, "theirs", "ingest", "hash-b")
	if err != nil {
		t.Fatal(err)
	}

	if err := ps.RevokeAPIKey(pid, theirs); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking another project's key = %v, want ErrNotFound", err)
	}
	if err := ps.RevokeAPIKey(pid, mine); err != nil {
		t.Fatal(err)
	}
	if keys, _ := ps.APIKeys(pid); len(keys) != 0 {
		t.Errorf("%d keys left, want 0", len(keys))
	}
	if keys, _ := ps.APIKeys(other.ID); len(keys) != 1 {
		t.Errorf("other project has %d keys, want 1", len(keys))
	}
}
