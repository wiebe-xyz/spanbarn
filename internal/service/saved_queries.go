package service

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

// SavedQueryRepository is the storage SavedQueryService needs.
type SavedQueryRepository interface {
	ListSavedQueries(projectID int64) ([]repository.SavedQuery, error)
	CreateSavedQuery(q repository.SavedQuery) (int64, error)
	DeleteSavedQuery(id int64) error
}

// SavedQueryService manages saved trace searches.
type SavedQueryService struct{ repo SavedQueryRepository }

func NewSavedQueryService(repo SavedQueryRepository) *SavedQueryService {
	return &SavedQueryService{repo: repo}
}

func (s *SavedQueryService) List(projectID int64) ([]SavedQuery, error) {
	return s.repo.ListSavedQueries(projectID)
}
func (s *SavedQueryService) Create(q SavedQuery) (int64, error) { return s.repo.CreateSavedQuery(q) }
func (s *SavedQueryService) Delete(id int64) error              { return s.repo.DeleteSavedQuery(id) }
