package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleSetupRejectsNonGet verifies the public setup endpoint only serves
// GET. Mutating methods must be refused before any project/key writes happen, so
// the endpoint cannot be driven as a write amplifier.
func TestHandleSetupRejectsNonGet(t *testing.T) {
	s := &Server{logger: slog.Default()}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(m, "/api/v1/setup/myproj", nil)
		rec := httptest.NewRecorder()
		s.handleSetup(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: want 405, got %d", m, rec.Code)
		}
	}
}

// TestSetupKeyDeterministicAndSecretDependent confirms the setup key is derived
// from (secret, slug): stable for a given secret, and different when the secret
// changes — so a strong mandatory secret keeps it unforgeable.
func TestSetupKeyDeterministicAndSecretDependent(t *testing.T) {
	a1, _ := setupKey("secret-A", "proj")
	a2, _ := setupKey("secret-A", "proj")
	b1, _ := setupKey("secret-B", "proj")
	if a1 != a2 {
		t.Errorf("setupKey not deterministic: %q vs %q", a1, a2)
	}
	if a1 == b1 {
		t.Error("setupKey must change when the secret changes")
	}
	if len(a1) != 40 {
		t.Errorf("setup key plaintext length = %d, want 40", len(a1))
	}
}
