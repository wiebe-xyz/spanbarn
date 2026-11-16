package service

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

// TraceExclusionRepository is the storage TraceExclusionService needs.
type TraceExclusionRepository interface {
	ListTraceExclusions(projectID int64) ([]repository.TraceExclusion, error)
	CreateTraceExclusion(projectID int64, operation string) (int64, error)
	DeleteTraceExclusion(id int64) error
}

// TraceExclusionService manages operations hidden from trace views.
type TraceExclusionService struct{ repo TraceExclusionRepository }

func NewTraceExclusionService(repo TraceExclusionRepository) *TraceExclusionService {
	return &TraceExclusionService{repo: repo}
}

func (s *TraceExclusionService) List(projectID int64) ([]repository.TraceExclusion, error) {
	return s.repo.ListTraceExclusions(projectID)
}
func (s *TraceExclusionService) Create(projectID int64, operation string) (int64, error) {
	return s.repo.CreateTraceExclusion(projectID, operation)
}
func (s *TraceExclusionService) Delete(id int64) error { return s.repo.DeleteTraceExclusion(id) }
