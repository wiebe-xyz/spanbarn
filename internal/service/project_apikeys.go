package service

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidAPIKey marks a key request with an empty, reserved or overlong
// name or a scope the API does not mint.
var ErrInvalidAPIKey = errors.New("invalid api key request")

// maxAPIKeyName bounds a key name, which the settings page shows in a row.
const maxAPIKeyName = 64

// CreateAPIKey registers a key for an existing project and returns its id.
// The caller generates the key and passes only its hash. The API mints
// ingest and read keys; a full key stays a CLI operation. The name "setup" is
// reserved for the setup flow's key, which migrations and revocation select
// by that name.
func (s *ProjectService) CreateAPIKey(projectID int64, name, scope, keyHash string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxAPIKeyName || name == "setup" {
		return 0, fmt.Errorf("%w: name must be 1-%d characters and not \"setup\"", ErrInvalidAPIKey, maxAPIKeyName)
	}
	if scope != "ingest" && scope != "read" {
		return 0, fmt.Errorf("%w: scope must be ingest or read", ErrInvalidAPIKey)
	}
	// Any lookup failure reads as a missing project, as Approve's does.
	if _, err := s.repo.GetProjectByID(projectID); err != nil {
		return 0, fmt.Errorf("%w: project %d: %w", ErrNotFound, projectID, err)
	}
	return s.repo.CreateAPIKey(projectID, name, keyHash, scope)
}

// RevokeAPIKey deletes one of a project's keys. A key that belongs to
// another project is ErrNotFound, so a project id in the path cannot reach
// keys outside it.
func (s *ProjectService) RevokeAPIKey(projectID, keyID int64) error {
	keys, err := s.repo.ListAPIKeys(projectID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.ID == keyID {
			return s.repo.RevokeAPIKey(keyID)
		}
	}
	return ErrNotFound
}

// HoldsSetupKey reports whether the project still has a setup key with this
// hash. Projects set up before the setup flow minted random keys hold a key
// derived from the slug until that key is rotated and revoked.
func (s *ProjectService) HoldsSetupKey(projectID int64, keyHash string) (bool, error) {
	keys, err := s.repo.ListAPIKeys(projectID)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if k.Name == "setup" && k.KeyHash == keyHash {
			return true, nil
		}
	}
	return false, nil
}
