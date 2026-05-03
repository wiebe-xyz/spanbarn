package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
)

// ErrUnauthorized is returned when an API key is not valid.
var ErrUnauthorized = errors.New("unauthorized")

// KeyLookup abstracts the repository methods needed for API key auth.
type KeyLookup interface {
	GetAPIKeyByHash(keyHash string) (APIKeyRecord, error)
	TouchAPIKey(id int64) error
}

// APIKeyRecord holds the fields returned by a key lookup.
// Mirrors repository.APIKey but avoids importing the repository package.
type APIKeyRecord struct {
	ID        int64
	ProjectID int64
	Scope     string
}

// Authorizer validates API keys against a static hash and/or the database.
type Authorizer struct {
	staticHash []byte // raw SHA-256 bytes (32 bytes)
	repo       KeyLookup
	logger     *slog.Logger
}

// NewAuthorizer creates an Authorizer. staticKeyHash is the hex-encoded SHA-256
// of the static env key (may be empty to skip static key auth). repo may be nil
// to skip DB key lookup.
func NewAuthorizer(staticKeyHash string, repo KeyLookup, logger *slog.Logger) *Authorizer {
	var hashBytes []byte
	staticKeyHash = strings.TrimSpace(staticKeyHash)
	if staticKeyHash != "" {
		decoded, err := hex.DecodeString(staticKeyHash)
		if err == nil && len(decoded) == sha256.Size {
			hashBytes = decoded
		} else if logger != nil {
			logger.Warn("invalid static API key hash, ignoring", "error", err)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Authorizer{staticHash: hashBytes, repo: repo, logger: logger}
}

// Authorize checks the provided raw API key. On success it returns the
// associated project ID (0 for static/admin key) and scope ("full" for static).
func (a *Authorizer) Authorize(key string) (projectID int64, scope string, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, "", ErrUnauthorized
	}

	sum := sha256.Sum256([]byte(key))

	// Check static env-var hash first (admin/global access).
	if len(a.staticHash) == sha256.Size {
		if subtle.ConstantTimeCompare(sum[:], a.staticHash) == 1 {
			return 0, "full", nil
		}
	}

	// Check DB-stored keys.
	if a.repo != nil {
		hexHash := hex.EncodeToString(sum[:])
		rec, err := a.repo.GetAPIKeyByHash(hexHash)
		if err == nil {
			// Touch last_used_at asynchronously-safe (ignore error).
			if touchErr := a.repo.TouchAPIKey(rec.ID); touchErr != nil {
				a.logger.Warn("failed to touch API key", "id", rec.ID, "error", touchErr)
			}
			return rec.ProjectID, rec.Scope, nil
		}
	}

	return 0, "", ErrUnauthorized
}

// HashKey returns the hex-encoded SHA-256 hash of a raw API key.
// Useful for storing keys and for tests.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
