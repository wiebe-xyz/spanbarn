package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func iamCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// TestSetIAMTokenCookiesExtendsAccessLifetimeWithRefresh guards against the
// exact bug this refresh flow exists to fix: if the access-token cookie were
// given the access token's real ~15m Expires, the browser would delete it
// before any request could trigger handleIAMProxy's reactive refresh, and
// the user would be forced back through a full login every 15 minutes
// anyway. When a refresh_token is issued, the access cookie must outlive
// that 15-minute window so the silent-refresh path actually gets a chance to
// run.
func TestSetIAMTokenCookiesExtendsAccessLifetimeWithRefresh(t *testing.T) {
	rec := httptest.NewRecorder()
	shortExpiry := time.Now().Add(15 * time.Minute)
	setIAMTokenCookies(rec, "access-1", "refresh-1", shortExpiry, true)

	access := iamCookie(rec, iamAccessCookie)
	if access == nil {
		t.Fatal("access cookie not set")
	}
	if !access.Expires.After(time.Now().Add(24 * time.Hour)) {
		t.Errorf("access cookie Expires = %v, want far beyond the 15m access-token lifetime (%v) since a refresh_token was issued", access.Expires, shortExpiry)
	}

	refresh := iamCookie(rec, iamRefreshCookie)
	if refresh == nil {
		t.Fatal("refresh cookie not set")
	}
	if refresh.Value != "refresh-1" {
		t.Errorf("refresh cookie value = %q, want refresh-1", refresh.Value)
	}
}

// TestSetIAMTokenCookiesKeepsShortExpiryWithoutRefresh covers the degraded
// case (offline_access not granted): there's no refresh token to renew with,
// so the access cookie keeps its real, short expiry — same as before this
// change.
func TestSetIAMTokenCookiesKeepsShortExpiryWithoutRefresh(t *testing.T) {
	rec := httptest.NewRecorder()
	shortExpiry := time.Now().Add(15 * time.Minute)
	setIAMTokenCookies(rec, "access-1", "", shortExpiry, true)

	access := iamCookie(rec, iamAccessCookie)
	if access == nil {
		t.Fatal("access cookie not set")
	}
	if access.Expires.After(time.Now().Add(time.Hour)) {
		t.Errorf("access cookie Expires = %v, want close to the real access-token expiry %v (no refresh_token to extend it)", access.Expires, shortExpiry)
	}
	if iamCookie(rec, iamRefreshCookie) != nil {
		t.Error("refresh cookie should not be set when refreshToken is empty")
	}
}

func TestClearIAMTokenCookies(t *testing.T) {
	rec := httptest.NewRecorder()
	clearIAMTokenCookies(rec, true)

	for _, name := range []string{iamAccessCookie, iamRefreshCookie} {
		c := iamCookie(rec, name)
		if c == nil {
			t.Fatalf("expected %s to be cleared, got no cookie", name)
		}
		if c.MaxAge >= 0 {
			t.Errorf("%s MaxAge = %d, want negative (deleted)", name, c.MaxAge)
		}
	}
}
