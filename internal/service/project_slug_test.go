package service

import (
	"database/sql"
	"errors"
	"testing"
)

// A slug that names an environment or starts with rr- is refused with an
// error that says where the value belongs; product names pass (#558).
func TestValidateProjectSlug(t *testing.T) {
	for _, slug := range []string{
		"main-prod", "main-production", "main-staging", "main-stage", "main-test",
		"main-testing", "main-e2e", "main-dev", "Main-PROD", "rr-backend", "languagetrainer-prod",
	} {
		if err := ValidateProjectSlug(slug); !errors.Is(err, ErrInvalidProjectSlug) {
			t.Errorf("ValidateProjectSlug(%q) = %v, want ErrInvalidProjectSlug", slug, err)
		}
	}
	for _, slug := range []string{
		"main", "languagetrainer", "profotograaf", "spanbarn", "rapid-root", "product", "devtools", "testbench", "prodigy",
	} {
		if err := ValidateProjectSlug(slug); err != nil {
			t.Errorf("ValidateProjectSlug(%q) = %v, want nil", slug, err)
		}
	}
}

// Create and EnsurePending apply the guard before any write.
func TestProjectCreateRefusesEnvironmentSlug(t *testing.T) {
	store, _ := adminStore(t)
	ps := NewProjectService(store)
	if _, err := ps.Create("shop-staging", "shop"); !errors.Is(err, ErrInvalidProjectSlug) {
		t.Errorf("Create = %v, want ErrInvalidProjectSlug", err)
	}
	if _, err := ps.EnsurePending("shop-prod", "shop"); !errors.Is(err, ErrInvalidProjectSlug) {
		t.Errorf("EnsurePending = %v, want ErrInvalidProjectSlug", err)
	}
	if _, err := ps.BySlug("shop-staging"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("BySlug after refusal = %v, want sql.ErrNoRows", err)
	}
	if _, err := ps.Create("shop", "shop"); err != nil {
		t.Errorf("Create(shop) = %v", err)
	}
}

// SetKeyEnvironment stores a normalised environment on a key, reads back
// through the key list, clears on an empty value and
// reports a missing key.
func TestSetKeyEnvironment(t *testing.T) {
	store, pid := adminStore(t)
	ps := NewProjectService(store)
	id, err := ps.IssueAPIKey(pid, "site", "hash-env", "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SetKeyEnvironment(id, " Staging "); err != nil {
		t.Fatal(err)
	}
	keys, err := ps.APIKeys(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Environment != "staging" {
		t.Fatalf("keys = %+v, want one with environment staging", keys)
	}
	if err := ps.SetKeyEnvironment(id, ""); err != nil {
		t.Fatal(err)
	}
	if keys, _ = ps.APIKeys(pid); keys[0].Environment != "" {
		t.Errorf("environment after clear = %q", keys[0].Environment)
	}
	if err := ps.SetKeyEnvironment(id+100, "staging"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing key = %v, want sql.ErrNoRows", err)
	}
}
