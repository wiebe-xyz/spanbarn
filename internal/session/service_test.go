package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/session"
)

func newService(t *testing.T) *session.Service {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return session.NewService(repository.NewRepository(db.DB), 3600, 3600, nil)
}

func TestLocalSessionLifecycle(t *testing.T) {
	svc := newService(t)
	token, expires, err := svc.Create("admin", "local", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := time.Until(expires); got > svc.TTL() || got <= 0 {
		t.Fatalf("expiry %v outside the TTL %v", got, svc.TTL())
	}

	res, err := svc.Authenticate(context.Background(), token)
	if err != nil || res.Session.Username != "admin" || res.RefreshDue {
		t.Fatalf("Authenticate: %+v, %v", res, err)
	}

	if url := svc.Logout(context.Background(), token); url != "" {
		t.Fatalf("local logout returned a URL %q", url)
	}
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("after logout want ErrInvalid, got %v", err)
	}
}

func TestUnknownAndEmptyTokensAreInvalid(t *testing.T) {
	svc := newService(t)
	for _, token := range []string{"", "nope"} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("token %q: want ErrInvalid, got %v", token, err)
		}
	}
}

func TestAbsoluteCapEndsSession(t *testing.T) {
	svc := newService(t)
	token, _, err := svc.Create("admin", "local", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("want ErrInvalid past the cap, got %v", err)
	}
}

func TestRevokeByIdpSidAndSub(t *testing.T) {
	svc := newService(t)
	mint := func(sub, sid string) string {
		token, _, err := svc.Create("u-"+sub, "oidc", &session.OIDCData{
			Claims:          auth.OIDCClaims{Subject: sub, SessionID: sid},
			AccessToken:     "at",
			RefreshToken:    "rt",
			AccessExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return token
	}
	bySid, bySub := mint("s1", "sid-1"), mint("s2", "sid-2")

	if n, err := svc.RevokeByIdpSid("sid-1"); err != nil || n != 1 {
		t.Fatalf("RevokeByIdpSid: %d, %v", n, err)
	}
	if n, err := svc.RevokeByIdpSub("s2"); err != nil || n != 1 {
		t.Fatalf("RevokeByIdpSub: %d, %v", n, err)
	}
	for _, token := range []string{bySid, bySub} {
		if _, err := svc.Authenticate(context.Background(), token); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("revoked session still valid: %v", err)
		}
	}
}
