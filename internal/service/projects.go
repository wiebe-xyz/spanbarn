package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

// ProjectRepository is the storage ProjectService needs.
type ProjectRepository interface {
	ListProjects() ([]repository.Project, error)
	GetProjectByID(id int64) (repository.Project, error)
	GetProjectBySlug(slug string) (repository.Project, error)
	EnsureProjectPending(slug, name string) (repository.Project, error)
	EnsureSetupAPIKey(projectID int64, keySHA256 string) error
	ApproveProject(id int64) (repository.Project, error)
	DeleteProject(id int64) error
	SetProjectE2E(id int64, enabled bool) error
	ProjectUsageStatsAll(hours int) ([]repository.ProjectUsageStats, error)
	CreateProject(slug, name string) (repository.Project, error)
	ListAPIKeys(projectID int64) ([]repository.APIKey, error)
	ListAllAPIKeys() ([]repository.APIKey, error)
	CreateAPIKey(projectID int64, name, keyHash, scope string) (int64, error)
	RevokeAPIKey(id int64) error
	UpsertE2EUser(username string, expiresAt time.Time) (repository.User, error)
}

// ProjectService manages projects, their API keys and E2E accounts.
type ProjectService struct{ repo ProjectRepository }

func NewProjectService(repo ProjectRepository) *ProjectService { return &ProjectService{repo: repo} }

func (s *ProjectService) List() ([]repository.Project, error) { return s.repo.ListProjects() }
func (s *ProjectService) ByID(id int64) (repository.Project, error) {
	return s.repo.GetProjectByID(id)
}
func (s *ProjectService) BySlug(slug string) (repository.Project, error) {
	return s.repo.GetProjectBySlug(slug)
}
func (s *ProjectService) EnsurePending(slug, name string) (repository.Project, error) {
	return s.repo.EnsureProjectPending(slug, name)
}
func (s *ProjectService) EnsureSetupAPIKey(projectID int64, keySHA256 string) error {
	return s.repo.EnsureSetupAPIKey(projectID, keySHA256)
}
func (s *ProjectService) Approve(id int64) (repository.Project, error) {
	return s.repo.ApproveProject(id)
}
func (s *ProjectService) Delete(id int64) error { return s.repo.DeleteProject(id) }
func (s *ProjectService) SetE2E(id int64, enabled bool) error {
	return s.repo.SetProjectE2E(id, enabled)
}

// UsageStats returns per-project span counts over the last hours hours, never nil.
func (s *ProjectService) UsageStats(hours int) ([]ProjectUsageStats, error) {
	stats, err := s.repo.ProjectUsageStatsAll(hours)
	if err != nil {
		return nil, err
	}
	if stats == nil {
		stats = []ProjectUsageStats{}
	}
	return stats, nil
}

func (s *ProjectService) APIKeys(projectID int64) ([]repository.APIKey, error) {
	return s.repo.ListAPIKeys(projectID)
}

// UpsertE2EUser creates or extends the E2E account for username.
func (s *ProjectService) UpsertE2EUser(username string, expiresAt time.Time) (repository.User, error) {
	return s.repo.UpsertE2EUser(username, expiresAt)
}

// The CLI manages projects and keys without the API's validation: it mints
// full-scope keys and revokes by key id alone.

// Create registers an approved project.
func (s *ProjectService) Create(slug, name string) (repository.Project, error) {
	return s.repo.CreateProject(slug, name)
}

// AllAPIKeys lists the keys of every project.
func (s *ProjectService) AllAPIKeys() ([]repository.APIKey, error) {
	return s.repo.ListAllAPIKeys()
}

// IssueAPIKey stores a key hash for projectID with any scope and no checks.
func (s *ProjectService) IssueAPIKey(projectID int64, name, keyHash, scope string) (int64, error) {
	return s.repo.CreateAPIKey(projectID, name, keyHash, scope)
}

// RevokeKey deletes a key by id, whatever project owns it.
func (s *ProjectService) RevokeKey(keyID int64) error { return s.repo.RevokeAPIKey(keyID) }
