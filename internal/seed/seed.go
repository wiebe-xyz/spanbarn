// Package seed registers projects and API keys declared in the deployment
// config, so a rebuilt database re-registers its clients instead of rejecting
// them.
//
// Projects and api_keys are ordinary rows with no declarative source. When the
// testing and staging databases were rebuilt on 2026-07-13 only the spanbarn
// self-project was re-seeded, so every client app's key vanished and all of
// them started failing 401 — silently, because an OTLP exporter has nowhere to
// report to when its telemetry backend is the thing rejecting it. Seeding on
// boot closes that hole.
//
// Seeds carry the SHA-256 hash of a key, never the key itself. A hash cannot
// authenticate anything (Authorize hashes the presented key and compares), so
// seeds are safe to keep in a ConfigMap or the repo, while the key itself stays
// in the client's own secret store.
package seed

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// sha256HexLen is the length of a hex-encoded SHA-256 digest, the format
// auth.HashKey emits and the only thing Authorize will ever match against.
const sha256HexLen = 64

// validScopes mirrors the scopes `spanbarn apikey create` accepts.
var validScopes = map[string]bool{"ingest": true, "read": true, "full": true}

// Key is one declared project + API key pairing.
type Key struct {
	Project   string `json:"project"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	KeySHA256 string `json:"key_sha256"`
}

// Store is the subset of the repository seeding needs.
type Store interface {
	EnsureProject(slug, name string) (repository.Project, error)
	EnsureAPIKey(projectID int64, name, keyHash, scope string) (bool, error)
}

// Parse decodes and validates SPANBARN_SEED_KEYS. An empty string yields no
// keys and no error, so seeding is opt-in.
//
// Validation is deliberately strict. Every field here fails silently at
// runtime if it is wrong — a mistyped hash simply never matches any presented
// key, and the operator sees an unexplained 401 rather than a config error. So
// a bad seed must fail loudly at boot, where it is attributable.
func Parse(raw string) ([]Key, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var keys []Key
	if err := json.Unmarshal([]byte(raw), &keys); err != nil {
		return nil, fmt.Errorf("parse seed keys: %w", err)
	}
	seen := make(map[string]bool, len(keys))
	for i := range keys {
		if err := keys[i].normalize(); err != nil {
			return nil, fmt.Errorf("seed key %d: %w", i, err)
		}
		if seen[keys[i].KeySHA256] {
			return nil, fmt.Errorf("seed key %d: duplicate key_sha256", i)
		}
		seen[keys[i].KeySHA256] = true
	}
	return keys, nil
}

// normalize trims and lowercases in place, then validates.
func (k *Key) normalize() error {
	k.Project = strings.TrimSpace(k.Project)
	k.Name = strings.TrimSpace(k.Name)
	k.Scope = strings.TrimSpace(k.Scope)
	// Hashes are compared as lowercase hex; an uppercase digest would be a
	// valid SHA-256 that never matches anything.
	k.KeySHA256 = strings.ToLower(strings.TrimSpace(k.KeySHA256))

	if k.Project == "" {
		return fmt.Errorf("project is required")
	}
	if k.Name == "" {
		return fmt.Errorf("name is required")
	}
	if k.Scope == "" {
		k.Scope = "ingest"
	}
	if !validScopes[k.Scope] {
		return fmt.Errorf("invalid scope %q (want ingest|read|full)", k.Scope)
	}
	if len(k.KeySHA256) != sha256HexLen {
		return fmt.Errorf("key_sha256 must be %d hex chars, got %d", sha256HexLen, len(k.KeySHA256))
	}
	if _, err := hex.DecodeString(k.KeySHA256); err != nil {
		return fmt.Errorf("key_sha256 is not hex: %w", err)
	}
	return nil
}

// Apply registers every declared project and key. It is idempotent: on a
// database that already has them it inserts nothing and reports 0 added.
//
// Errors are returned rather than logged-and-swallowed. Seeding runs at boot
// before serving, so a failure here means clients would authenticate against an
// incomplete key set — better to fail the process than to come up and 401
// everyone.
func Apply(store Store, keys []Key, logger *slog.Logger) (added int, err error) {
	for _, k := range keys {
		proj, err := store.EnsureProject(k.Project, k.Project)
		if err != nil {
			return added, fmt.Errorf("ensure project %q: %w", k.Project, err)
		}
		inserted, err := store.EnsureAPIKey(proj.ID, k.Name, k.KeySHA256, k.Scope)
		if err != nil {
			return added, fmt.Errorf("ensure api key %q: %w", k.Name, err)
		}
		if inserted {
			added++
			if logger != nil {
				logger.Info("seed: registered API key",
					"project", k.Project, "name", k.Name, "scope", k.Scope, "project_id", proj.ID)
			}
		}
	}
	return added, nil
}
