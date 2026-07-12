package auth

import (
	"strings"
	"testing"
)

func TestNewSessionTokenIsOpaqueAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := NewSessionToken()
		if len(tok) < 40 {
			t.Fatalf("token too short: %d chars", len(tok))
		}
		if strings.Contains(tok, ".") {
			// A dot would collide with the JWT-vs-session disambiguation on
			// the Bearer path (JWTs have two dots).
			t.Fatalf("token contains '.': %q", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = true
	}
}

func TestHashSessionToken(t *testing.T) {
	h1 := HashSessionToken("abc")
	h2 := HashSessionToken("abc")
	h3 := HashSessionToken("abd")
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Fatal("different tokens must not collide trivially")
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(h1))
	}
	// Whitespace around a cookie value must not change the key.
	if HashSessionToken(" abc \n") != h1 {
		t.Fatal("hash must trim surrounding whitespace")
	}
}
