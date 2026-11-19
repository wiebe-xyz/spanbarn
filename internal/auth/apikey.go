package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
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
	ID          int64
	ProjectID   int64
	Scope       string
	Environment string
}

// Identity is what a valid key stands for: the project it is bound to, its
// scope and the environment stamped on records that carry none of their own.
type Identity struct {
	ProjectID   int64
	Scope       string
	Environment string
	// KeyID is the api_keys row the key matched. The static key has no row and
	// reads 0.
	KeyID int64
}

// Authorizer validates API keys against a static hash and/or the database.
type Authorizer struct {
	staticHash []byte // raw SHA-256 bytes (32 bytes)
	repo       KeyLookup
	logger     *slog.Logger
	rejections RejectionSink
}

// SetRejectionSink makes the authorizer report every request whose key no
// stored key matches (#572). Call it before the authorizer serves requests.
func (a *Authorizer) SetRejectionSink(s RejectionSink) { a.rejections = s }

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
	id, err := a.AuthorizeIdentity(key)
	return id.ProjectID, id.Scope, err
}

// AuthorizeIdentity is Authorize with the key's environment included. The
// static key has no environment.
func (a *Authorizer) AuthorizeIdentity(key string) (Identity, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Identity{}, ErrUnauthorized
	}

	sum := sha256.Sum256([]byte(key))

	// Check static env-var hash first (admin/global access).
	if len(a.staticHash) == sha256.Size {
		if subtle.ConstantTimeCompare(sum[:], a.staticHash) == 1 {
			return Identity{Scope: "full"}, nil
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
			return Identity{ProjectID: rec.ProjectID, Scope: rec.Scope, Environment: rec.Environment, KeyID: rec.ID}, nil
		}
		if errors.Is(err, ErrKeyNotFound) && a.rejections != nil {
			a.rejections.Reject(hexHash[:KeyFingerprintLen])
		}
	}

	return Identity{}, ErrUnauthorized
}

// NewAPIKey returns a new API key and its hash: 32 bytes from crypto/rand,
// hex-encoded. Only the hash is stored.
func NewAPIKey() (key, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate API key: %w", err)
	}
	key = hex.EncodeToString(b)
	return key, HashKey(key), nil
}

// HashKey returns the hex-encoded SHA-256 hash of a raw API key.
// Useful for storing keys and for tests.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
