package auth

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockKeyLookup implements KeyLookup for testing.
type mockKeyLookup struct {
	keys    map[string]APIKeyRecord // keyed by hash
	touched atomic.Int64
}

func newMockKeyLookup() *mockKeyLookup {
	return &mockKeyLookup{keys: make(map[string]APIKeyRecord)}
}

func (m *mockKeyLookup) GetAPIKeyByHash(hash string) (APIKeyRecord, error) {
	if rec, ok := m.keys[hash]; ok {
		return rec, nil
	}
	return APIKeyRecord{}, errors.New("not found")
}

func (m *mockKeyLookup) TouchAPIKey(id int64) error {
	m.touched.Add(1)
	return nil
}

func TestAuthorizeStaticKey(t *testing.T) {
	rawKey := "my-secret-api-key"
	hash := HashKey(rawKey)

	a := NewAuthorizer(hash, nil, nil)
	pid, scope, err := a.Authorize(rawKey)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if pid != 0 {
		t.Errorf("expected projectID 0, got %d", pid)
	}
	if scope != "full" {
		t.Errorf("expected scope 'full', got %q", scope)
	}
}

func TestAuthorizeWrongKey(t *testing.T) {
	hash := HashKey("correct-key")
	a := NewAuthorizer(hash, nil, nil)

	_, _, err := a.Authorize("wrong-key")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorizeDBKey(t *testing.T) {
	rawKey := "db-stored-key"
	keyHash := HashKey(rawKey)

	repo := newMockKeyLookup()
	repo.keys[keyHash] = APIKeyRecord{ID: 42, ProjectID: 7, Scope: "ingest"}

	a := NewAuthorizer("", repo, nil) // no static key
	pid, scope, err := a.Authorize(rawKey)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if pid != 7 {
		t.Errorf("expected projectID 7, got %d", pid)
	}
	if scope != "ingest" {
		t.Errorf("expected scope 'ingest', got %q", scope)
	}
}

func TestAuthorizeTouchesLastUsed(t *testing.T) {
	rawKey := "touch-test-key"
	keyHash := HashKey(rawKey)

	repo := newMockKeyLookup()
	repo.keys[keyHash] = APIKeyRecord{ID: 1, ProjectID: 1, Scope: "ingest"}

	a := NewAuthorizer("", repo, nil)
	_, _, err := a.Authorize(rawKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.touched.Load() != 1 {
		t.Errorf("expected TouchAPIKey called once, got %d", repo.touched.Load())
	}
}

func TestAuthorizeEmptyKey(t *testing.T) {
	a := NewAuthorizer(HashKey("key"), nil, nil)
	_, _, err := a.Authorize("")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for empty key, got %v", err)
	}
}

func TestAuthorizeTimingSafe(t *testing.T) {
	hash := HashKey("timing-test-key")
	a := NewAuthorizer(hash, nil, nil)

	// Basic timing check: both valid and invalid should complete in similar time.
	// This is a basic sanity check, not a rigorous timing analysis.
	iterations := 100

	start := time.Now()
	for range iterations {
		a.Authorize("timing-test-key")
	}
	validDuration := time.Since(start)

	start = time.Now()
	for range iterations {
		a.Authorize("wrong-key-value!")
	}
	invalidDuration := time.Since(start)

	// Allow 10x ratio -- we're just checking constant-time compare is used,
	// not doing a rigorous timing analysis.
	ratio := float64(validDuration) / float64(invalidDuration)
	if ratio > 10 || ratio < 0.1 {
		t.Errorf("suspicious timing difference: valid=%v invalid=%v ratio=%.2f", validDuration, invalidDuration, ratio)
	}
}
