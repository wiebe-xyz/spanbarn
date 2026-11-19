package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func TestAPIKeyRejectionsListsRevokedAndUnknownKeys(t *testing.T) {
	cfg := config.Config{DBPath: filepath.Join(t.TempDir(), "spanbarn.db")}
	store, err := engine.OpenStorage(context.Background(), cfg.DBPath, engine.StorageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.ForStorage(store)
	project, err := repo.CreateProject("shop", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.CreateAPIKey(project.ID, "site", "1d4e4d34aaaaaaaa", "ingest")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeAPIKey(id); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.AddKeyRejections([]model.KeyRejection{
		{Fingerprint: "1d4e4d34", Count: 4, LastSeen: now.Unix()},
		{Fingerprint: "ffffffff", Count: 1, LastSeen: now.Unix()},
	}, now); err != nil {
		t.Fatal(err)
	}
	store.Close()

	if err := runAPIKeyRejections(cfg, nil); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := runAPIKeyRejections(cfg, []string{"--since=1h"}); err != nil {
		t.Fatalf("list with since: %v", err)
	}
	if err := runAPIKeyRejections(cfg, []string{"--since=soon"}); err == nil {
		t.Error("a since that is no duration was accepted")
	}
}
