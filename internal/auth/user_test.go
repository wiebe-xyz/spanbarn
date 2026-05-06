package auth

import (
	"errors"
	"testing"
)

// mockUserLookup implements UserLookup for testing.
type mockUserLookup struct {
	users map[string]UserRecord
}

func newMockUserLookup() *mockUserLookup {
	return &mockUserLookup{users: make(map[string]UserRecord)}
}

func (m *mockUserLookup) GetUserByUsername(username string) (UserRecord, error) {
	if u, ok := m.users[username]; ok {
		return u, nil
	}
	return UserRecord{}, errors.New("not found")
}

func TestHashAndVerifyPassword(t *testing.T) {
	password := "super-secret-123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if hash == password {
		t.Fatal("hash should not equal plaintext")
	}

	// Verify via a mock user lookup.
	repo := newMockUserLookup()
	repo.users["admin"] = UserRecord{ID: 1, Username: "admin", PasswordHash: hash}

	ua := NewUserAuthenticator(repo, nil)
	if err := ua.Authenticate("admin", password); err != nil {
		t.Fatalf("expected successful auth, got %v", err)
	}
}

func TestAuthenticateValidUser(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	repo := newMockUserLookup()
	repo.users["testuser"] = UserRecord{ID: 1, Username: "testuser", PasswordHash: hash}

	ua := NewUserAuthenticator(repo, nil)
	if err := ua.Authenticate("testuser", "correct-password"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestAuthenticateWrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct-password")
	repo := newMockUserLookup()
	repo.users["testuser"] = UserRecord{ID: 1, Username: "testuser", PasswordHash: hash}

	ua := NewUserAuthenticator(repo, nil)
	err := ua.Authenticate("testuser", "wrong-password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestAuthenticateNonexistentUser(t *testing.T) {
	repo := newMockUserLookup()
	ua := NewUserAuthenticator(repo, nil)

	err := ua.Authenticate("ghost", "password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestPasswordChangeDetection(t *testing.T) {
	oldPassword := "old-password"
	newPassword := "new-password"

	oldHash, _ := HashPassword(oldPassword)
	newHash, _ := HashPassword(newPassword)

	repo := newMockUserLookup()
	repo.users["admin"] = UserRecord{ID: 1, Username: "admin", PasswordHash: oldHash}
	ua := NewUserAuthenticator(repo, nil)

	// Old password works.
	if err := ua.Authenticate("admin", oldPassword); err != nil {
		t.Fatalf("old password should work: %v", err)
	}

	// Simulate bootstrap: detect mismatch and update.
	repo.users["admin"] = UserRecord{ID: 1, Username: "admin", PasswordHash: newHash}

	// New password works.
	if err := ua.Authenticate("admin", newPassword); err != nil {
		t.Fatalf("new password should work after update: %v", err)
	}

	// Old password no longer works.
	if err := ua.Authenticate("admin", oldPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old password should fail after update, got %v", err)
	}
}

func TestAuthenticateEmptyCredentials(t *testing.T) {
	repo := newMockUserLookup()
	ua := NewUserAuthenticator(repo, nil)

	if err := ua.Authenticate("", "pass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for empty username, got %v", err)
	}
	if err := ua.Authenticate("user", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for empty password, got %v", err)
	}
}
