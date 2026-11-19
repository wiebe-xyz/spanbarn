package repository

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"context"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func TestKeyFingerprintLenMatchesAuth(t *testing.T) {
	if KeyFingerprintLen != auth.KeyFingerprintLen {
		t.Fatalf("repository %d, auth %d: the two must name keys by the same prefix", KeyFingerprintLen, auth.KeyFingerprintLen)
	}
}

func TestAddKeyRejectionsAccumulatesAndKeepsFirstSeen(t *testing.T) {
	repo := setupTestDB(t)
	t0 := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	if err := repo.AddKeyRejections([]model.KeyRejection{{Fingerprint: "aaaaaaaa", Count: 3, LastSeen: t0.Unix()}}, t0); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(time.Hour)
	if err := repo.AddKeyRejections([]model.KeyRejection{
		{Fingerprint: "aaaaaaaa", Count: 4, LastSeen: t1.Unix()},
		{Fingerprint: "bbbbbbbb", Count: 1}, // no time: the store stamps its own
	}, t1); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddKeyRejections(nil, t1); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	rows, err := repo.ListKeyRejections(t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two fingerprints", rows)
	}
	var a KeyRejection
	for _, r := range rows {
		if r.Fingerprint == "aaaaaaaa" {
			a = r
		}
		if r.Revoked {
			t.Errorf("%s reads as revoked, but no key was revoked", r.Fingerprint)
		}
	}
	if a.Count != 7 || !a.FirstSeen.Equal(t0) || !a.LastSeen.Equal(t1) {
		t.Errorf("aaaaaaaa = %+v, want 7 requests from %s to %s", a, t0, t1)
	}
}

func TestAddKeyRejectionsDropsRowsPastTheRetention(t *testing.T) {
	repo := setupTestDB(t)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := repo.AddKeyRejections([]model.KeyRejection{{Fingerprint: "oldoldol", Count: 1, LastSeen: old.Unix()}}, old); err != nil {
		t.Fatal(err)
	}
	now := old.Add(KeyRejectionRetention + time.Hour)
	if err := repo.AddKeyRejections([]model.KeyRejection{{Fingerprint: "newnewne", Count: 1, LastSeen: now.Unix()}}, now); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListKeyRejections(old)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Fingerprint != "newnewne" {
		t.Errorf("rows = %+v, want only the fingerprint inside the retention", rows)
	}
}

func TestRevokedKeyReadsAsRevokedInTheCounters(t *testing.T) {
	repo := setupTestDB(t)
	project, err := repo.CreateProject("shop", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	key, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateAPIKey(project.ID, "site", hash, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeAPIKey(id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAPIKeyByHash(hash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked key lookup: %v, want no rows", err)
	}

	now := time.Now()
	fp := auth.HashKey(key)[:auth.KeyFingerprintLen]
	if err := repo.AddKeyRejections([]model.KeyRejection{{Fingerprint: fp, Count: 2, LastSeen: now.Unix()}, {Fingerprint: "unknown1", Count: 1, LastSeen: now.Unix()}}, now); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListKeyRejections(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byFP := map[string]KeyRejection{}
	for _, r := range rows {
		byFP[r.Fingerprint] = r
	}
	if r := byFP[fp]; !r.Revoked || r.KeyID != id || r.ProjectID != project.ID || r.KeyName != "site" || r.RevokedAt.IsZero() || r.Count != 2 {
		t.Errorf("revoked key = %+v, want its id %d, project %d and name site", r, id, project.ID)
	}
	if r := byFP["unknown1"]; r.Revoked {
		t.Errorf("a fingerprint of no key reads as revoked: %+v", r)
	}

	if err := repo.RevokeAPIKey(id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("revoking a revoked key: %v, want no rows", err)
	}
}

// The counters live in the database file, so a restart keeps them (#572).
func TestKeyRejectionsSurviveReopeningTheDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rej.db")
	open := func() (*Repository, func()) {
		db, err := engine.NewDB(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.MigrateSingleFile(context.Background(), db.DB); err != nil {
			t.Fatal(err)
		}
		return NewRepository(db.DB), func() { db.Close() }
	}
	repo, closeDB := open()
	now := time.Now()
	if err := repo.AddKeyRejections([]model.KeyRejection{{Fingerprint: "aaaaaaaa", Count: 5, LastSeen: now.Unix()}}, now); err != nil {
		t.Fatal(err)
	}
	closeDB()

	repo, closeDB = open()
	defer closeDB()
	rows, err := repo.ListKeyRejections(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 5 {
		t.Errorf("after reopening: %+v, want the 5 counted requests", rows)
	}
}
