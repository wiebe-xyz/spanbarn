package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateAndValidateSession(t *testing.T) {
	sm := NewSessionManager("test-secret", 3600)

	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if !strings.Contains(token, ".") {
		t.Fatal("token should contain a dot separator")
	}

	username, err := sm.Validate(token)
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if username != "admin" {
		t.Errorf("expected username 'admin', got %q", username)
	}
}

func TestSessionExpired(t *testing.T) {
	sm := NewSessionManager("test-secret", 1) // 1 second TTL

	// Override now to produce an already-expired token.
	sm.now = func() time.Time {
		return time.Now().Add(-2 * time.Second)
	}

	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Reset now to real time for validation.
	sm.now = time.Now

	_, err = sm.Validate(token)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
}

func TestSessionTampered(t *testing.T) {
	sm := NewSessionManager("test-secret", 3600)

	token, err := sm.Create("admin")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Tamper with the payload part.
	parts := strings.SplitN(token, ".", 2)
	tampered := parts[0] + "x" + "." + parts[1]

	_, err = sm.Validate(tampered)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expected ErrSessionInvalid for tampered token, got %v", err)
	}

	// Tamper with the signature part.
	tampered2 := parts[0] + "." + parts[1] + "x"
	_, err = sm.Validate(tampered2)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expected ErrSessionInvalid for tampered signature, got %v", err)
	}
}

func TestSessionInvalidFormat(t *testing.T) {
	sm := NewSessionManager("test-secret", 3600)

	cases := []string{
		"",
		"garbage",
		"no-dot-here",
		"...three.dots",
		"!!!.???",
	}
	for _, tc := range cases {
		_, err := sm.Validate(tc)
		if err == nil {
			t.Errorf("expected error for input %q, got nil", tc)
		}
	}
}

func TestSessionDifferentSecrets(t *testing.T) {
	sm1 := NewSessionManager("secret-one", 3600)
	sm2 := NewSessionManager("secret-two", 3600)

	token, err := sm1.Create("admin")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = sm2.Validate(token)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expected ErrSessionInvalid for wrong secret, got %v", err)
	}
}
