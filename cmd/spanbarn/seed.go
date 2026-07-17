package main

import (
	"fmt"
	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/seed"
)

// applySeedKeys registers the projects and API keys declared in
// SPANBARN_SEED_KEYS. It runs on the writer and standalone modes only, after
// migrations and before serving, so clients never authenticate against a
// half-populated key set.
//
// This exists because projects and api_keys are otherwise undeclared DB state:
// rebuilding a database silently deauthenticates every client, which is exactly
// what happened to testing and staging on 2026-07-13. Seeding is idempotent, so
// it is a no-op on an already-populated database and self-heals a rebuilt one.
func applySeedKeys(repo *repository.Repository, cfg config.Config, logger *slog.Logger) error {
	keys, err := seed.Parse(cfg.SeedKeys)
	if err != nil {
		return fmt.Errorf("seed keys: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	added, err := seed.Apply(repo, keys, logger)
	if err != nil {
		return fmt.Errorf("seed keys: %w", err)
	}
	logger.Info("seed: api keys ready", "declared", len(keys), "added", added)
	return nil
}
