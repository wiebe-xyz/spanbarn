package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func testWebSession(idHash string) WebSession {
	now := time.Now().Unix()
	return WebSession{
		IDHash:            idHash,
		Username:          "wiebe",
		AuthMethod:        "oidc",
		IdpSub:            "sub-1",
		IdpSid:            "sid-1",
		IDToken:           "idtok",
		AccessToken:       "at-1",
		RefreshToken:      "rt-1",
		AccessExpiresAt:   now + 900,
		ClaimsJSON:        `{"sub":"sub-1"}`,
		CreatedAt:         now,
		AbsoluteExpiresAt: now + 43200,
	}
}

func TestWebSessionCreateGetRoundtrip(t *testing.T) {
	repo := setupTestDB(t)

	want := testWebSession("hash-1")
	if err := repo.CreateWebSession(want); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}

	got, err := repo.GetWebSessionByIDHash("hash-1")
	if err != nil {
		t.Fatalf("GetWebSessionByIDHash: %v", err)
	}
	if got != want {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, want)
	}

	// Unknown handle → sql.ErrNoRows (forged/pruned cookie).
	if _, err := repo.GetWebSessionByIDHash("nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown hash: err = %v, want sql.ErrNoRows", err)
	}

	// Duplicate primary key must fail.
	if err := repo.CreateWebSession(want); err == nil {
		t.Fatal("expected duplicate id_hash to fail")
	}
}

func TestWebSessionLocalRowHasNullTokenColumns(t *testing.T) {
	repo := setupTestDB(t)

	now := time.Now().Unix()
	ws := WebSession{
		IDHash:            "hash-local",
		Username:          "admin",
		AuthMethod:        "local",
		CreatedAt:         now,
		AbsoluteExpiresAt: now + 3600,
	}
	if err := repo.CreateWebSession(ws); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}

	got, err := repo.GetWebSessionByIDHash("hash-local")
	if err != nil {
		t.Fatalf("GetWebSessionByIDHash: %v", err)
	}
	if got.AccessToken != "" || got.RefreshToken != "" || got.IDToken != "" || got.AccessExpiresAt != 0 {
		t.Fatalf("local session must have empty token fields, got %+v", got)
	}

	// The token columns must be stored as real NULLs, not empty strings.
	var nulls int
	err = repo.DB().QueryRow(`SELECT COUNT(*) FROM web_sessions
		WHERE id_hash = 'hash-local' AND id_token IS NULL AND access_token IS NULL
		AND refresh_token IS NULL AND access_expires_at IS NULL AND idp_sub IS NULL`).Scan(&nulls)
	if err != nil {
		t.Fatalf("null check: %v", err)
	}
	if nulls != 1 {
		t.Fatal("token columns of a local session must be NULL")
	}

	// Invalid auth_method is rejected by the CHECK constraint.
	bad := ws
	bad.IDHash = "hash-bad"
	bad.AuthMethod = "password"
	if err := repo.CreateWebSession(bad); err == nil {
		t.Fatal("expected CHECK constraint to reject unknown auth_method")
	}
}

func TestWebSessionUpdateTokens(t *testing.T) {
	repo := setupTestDB(t)

	ws := testWebSession("hash-2")
	if err := repo.CreateWebSession(ws); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if err := repo.MarkWebSessionRefreshFailing("hash-2", 111); err != nil {
		t.Fatalf("MarkWebSessionRefreshFailing: %v", err)
	}

	newExp := time.Now().Unix() + 1800
	if err := repo.UpdateWebSessionTokens("hash-2", "idtok-2", "at-2", "rt-2", newExp, `{"sub":"sub-1","roles":["op"]}`, 222); err != nil {
		t.Fatalf("UpdateWebSessionTokens: %v", err)
	}

	got, err := repo.GetWebSessionByIDHash("hash-2")
	if err != nil {
		t.Fatalf("GetWebSessionByIDHash: %v", err)
	}
	if got.AccessToken != "at-2" || got.RefreshToken != "rt-2" || got.IDToken != "idtok-2" {
		t.Fatalf("tokens not rotated: %+v", got)
	}
	if got.AccessExpiresAt != newExp || got.LastRefreshAt != 222 {
		t.Fatalf("timestamps not updated: %+v", got)
	}
	if got.RefreshFailingSince != 0 {
		t.Fatal("successful refresh must clear refresh_failing_since")
	}
	if got.ClaimsJSON != `{"sub":"sub-1","roles":["op"]}` {
		t.Fatalf("claims not re-snapshotted: %q", got.ClaimsJSON)
	}

	// Empty idToken/claims keep the previous values (refresh response
	// without an id_token must not wipe the snapshot).
	if err := repo.UpdateWebSessionTokens("hash-2", "", "at-3", "rt-3", newExp+900, "", 333); err != nil {
		t.Fatalf("UpdateWebSessionTokens (no id_token): %v", err)
	}
	got, _ = repo.GetWebSessionByIDHash("hash-2")
	if got.IDToken != "idtok-2" || got.ClaimsJSON != `{"sub":"sub-1","roles":["op"]}` {
		t.Fatalf("empty id_token/claims must not overwrite: %+v", got)
	}
	if got.AccessToken != "at-3" || got.RefreshToken != "rt-3" {
		t.Fatalf("tokens must still rotate: %+v", got)
	}

	// Updating a gone session reports sql.ErrNoRows so the caller can 401.
	if err := repo.UpdateWebSessionTokens("gone", "", "x", "y", 1, "", 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of missing row: err = %v, want sql.ErrNoRows", err)
	}
}

func TestWebSessionMarkRefreshFailingKeepsFirstStamp(t *testing.T) {
	repo := setupTestDB(t)

	if err := repo.CreateWebSession(testWebSession("hash-3")); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if err := repo.MarkWebSessionRefreshFailing("hash-3", 100); err != nil {
		t.Fatalf("mark 1: %v", err)
	}
	// A later retry must NOT move the stamp — grace counts from first failure.
	if err := repo.MarkWebSessionRefreshFailing("hash-3", 999); err != nil {
		t.Fatalf("mark 2: %v", err)
	}
	got, err := repo.GetWebSessionByIDHash("hash-3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RefreshFailingSince != 100 {
		t.Fatalf("refresh_failing_since = %d, want 100 (first failure wins)", got.RefreshFailingSince)
	}
}

func TestWebSessionDelete(t *testing.T) {
	repo := setupTestDB(t)

	if err := repo.CreateWebSession(testWebSession("hash-4")); err != nil {
		t.Fatalf("CreateWebSession: %v", err)
	}
	if err := repo.DeleteWebSession("hash-4"); err != nil {
		t.Fatalf("DeleteWebSession: %v", err)
	}
	if _, err := repo.GetWebSessionByIDHash("hash-4"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session not deleted: err = %v", err)
	}
	// Idempotent: deleting again is not an error.
	if err := repo.DeleteWebSession("hash-4"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestWebSessionDeleteByIdpSidAndSub(t *testing.T) {
	repo := setupTestDB(t)

	a := testWebSession("hash-a") // sub-1 / sid-1
	b := testWebSession("hash-b") // sub-1, different sid
	b.IdpSid = "sid-2"
	c := testWebSession("hash-c") // different user entirely
	c.IdpSub, c.IdpSid = "sub-2", "sid-3"
	local := WebSession{IDHash: "hash-l", Username: "admin", AuthMethod: "local",
		CreatedAt: 1, AbsoluteExpiresAt: time.Now().Unix() + 3600}
	for _, ws := range []WebSession{a, b, c, local} {
		if err := repo.CreateWebSession(ws); err != nil {
			t.Fatalf("CreateWebSession %s: %v", ws.IDHash, err)
		}
	}

	// By sid: only the exact IdP session dies.
	n, err := repo.DeleteWebSessionsByIdpSid("sid-1")
	if err != nil {
		t.Fatalf("DeleteWebSessionsByIdpSid: %v", err)
	}
	if n != 1 {
		t.Fatalf("delete by sid: n = %d, want 1", n)
	}
	if _, err := repo.GetWebSessionByIDHash("hash-b"); err != nil {
		t.Fatal("sibling session with another sid must survive")
	}

	// By sub: every remaining session of the subject dies; other subjects
	// and local sessions (idp_sub NULL) survive.
	n, err = repo.DeleteWebSessionsByIdpSub("sub-1")
	if err != nil {
		t.Fatalf("DeleteWebSessionsByIdpSub: %v", err)
	}
	if n != 1 {
		t.Fatalf("delete by sub: n = %d, want 1", n)
	}
	if _, err := repo.GetWebSessionByIDHash("hash-c"); err != nil {
		t.Fatal("other subject must survive")
	}
	if _, err := repo.GetWebSessionByIDHash("hash-l"); err != nil {
		t.Fatal("local session must survive an OIDC sub deletion")
	}
}

func TestDeleteExpiredWebSessions(t *testing.T) {
	repo := setupTestDB(t)

	now := time.Now()
	expired := testWebSession("hash-old")
	expired.AbsoluteExpiresAt = now.Add(-time.Minute).Unix()
	live := testWebSession("hash-live")
	live.AbsoluteExpiresAt = now.Add(time.Hour).Unix()
	for _, ws := range []WebSession{expired, live} {
		if err := repo.CreateWebSession(ws); err != nil {
			t.Fatalf("CreateWebSession: %v", err)
		}
	}

	n, err := repo.DeleteExpiredWebSessions(now)
	if err != nil {
		t.Fatalf("DeleteExpiredWebSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if _, err := repo.GetWebSessionByIDHash("hash-live"); err != nil {
		t.Fatal("live session must not be pruned")
	}
}
