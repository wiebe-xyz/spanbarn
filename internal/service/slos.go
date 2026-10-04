package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ErrConflict reports a write that collides with an existing row, such as an
// SLO name already used in the project.
var ErrConflict = repository.ErrConflict

const (
	maxSLOName       = 120
	minSLOWindowDays = 1
	maxSLOWindowDays = 90
	// DefaultBurnCooldownMinutes applies when a burn alert sets no cooldown.
	DefaultBurnCooldownMinutes = 30
)

// SLO and SLOBurnAlert are what the API returns.
type (
	SLO          = repository.SLO
	SLOBurnAlert = repository.SLOBurnAlert
)

// SLORepository is the storage SLOService needs. Every call is scoped to a
// project, so an id from another project reads as not found.
type SLORepository interface {
	ListSLOs(projectID int64) ([]repository.SLO, error)
	GetSLO(projectID, id int64) (*repository.SLO, error)
	CreateSLO(s repository.SLO) (int64, error)
	UpdateSLO(s repository.SLO) error
	DeleteSLO(projectID, id int64) error
	ListSLOBurnAlerts(projectID, sloID int64) ([]repository.SLOBurnAlert, error)
	GetSLOBurnAlert(projectID, id int64) (*repository.SLOBurnAlert, error)
	CreateSLOBurnAlert(projectID int64, a repository.SLOBurnAlert) (int64, error)
	UpdateSLOBurnAlert(projectID int64, a repository.SLOBurnAlert) error
	DeleteSLOBurnAlert(projectID, id int64) error
	SumSLOCounts(sloID int64, from, to time.Time) (good, total int64, err error)
}

// SLOInput is the editable part of an SLO. The filters use the filter model.
type SLOInput struct {
	Name        string
	GoodFilter  json.RawMessage
	TotalFilter json.RawMessage
	Target      float64
	WindowDays  int
}

// SLOService manages SLOs, their burn alerts and their status.
type SLOService struct {
	repo   SLORepository
	logger *slog.Logger
	now    func() time.Time
}

func NewSLOService(repo SLORepository, logger *slog.Logger) *SLOService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SLOService{repo: repo, logger: logger, now: time.Now}
}

// fail logs an unexpected error at error level. Not-found, conflict and
// validation errors are the caller's to see and are not logged.
func (s *SLOService) fail(msg string, err error, args ...any) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, filter.ErrInvalid) {
		return err
	}
	s.logger.Error(msg, append([]any{"error", err}, args...)...)
	return err
}

// normalizeFilter validates a stored filter and returns its canonical JSON.
// An empty filter is stored as an empty group, which matches every span.
func normalizeFilter(field string, raw json.RawMessage) (json.RawMessage, error) {
	expr, err := filter.Parse(string(raw))
	if err != nil {
		return nil, analyzeInvalid("%s: %v", field, err)
	}
	if expr == nil {
		return json.RawMessage(`{}`), nil
	}
	out, err := json.Marshal(expr)
	if err != nil {
		return nil, analyzeInvalid("%s: %v", field, err)
	}
	return out, nil
}

// validateSLO checks the input and returns the SLO it describes.
func validateSLO(projectID int64, in SLOInput) (repository.SLO, error) {
	name := strings.TrimSpace(in.Name)
	switch {
	case projectID <= 0:
		return repository.SLO{}, analyzeInvalid("project_id is required")
	case name == "":
		return repository.SLO{}, analyzeInvalid("name is required")
	case len(name) > maxSLOName:
		return repository.SLO{}, analyzeInvalid("name is limited to %d characters", maxSLOName)
	case !(in.Target > 0 && in.Target < 1):
		return repository.SLO{}, analyzeInvalid("target must be above 0 and below 1")
	case in.WindowDays < minSLOWindowDays || in.WindowDays > maxSLOWindowDays:
		return repository.SLO{}, analyzeInvalid("windowDays must be between %d and %d", minSLOWindowDays, maxSLOWindowDays)
	}
	good, err := normalizeFilter("goodFilter", in.GoodFilter)
	if err != nil {
		return repository.SLO{}, err
	}
	total, err := normalizeFilter("totalFilter", in.TotalFilter)
	if err != nil {
		return repository.SLO{}, err
	}
	return repository.SLO{
		ProjectID: projectID, Name: name, GoodFilter: good, TotalFilter: total,
		Target: in.Target, WindowDays: in.WindowDays,
	}, nil
}

// ListSLOs returns the SLOs of a project. A project without any returns an
// empty list.
func (s *SLOService) ListSLOs(projectID int64) ([]SLO, error) {
	if projectID <= 0 {
		return nil, analyzeInvalid("project_id is required")
	}
	slos, err := s.repo.ListSLOs(projectID)
	if err != nil {
		return nil, s.fail("list slos failed", err, "project_id", projectID)
	}
	if slos == nil {
		slos = []SLO{}
	}
	return slos, nil
}

// GetSLO returns one SLO of the project, or ErrNotFound.
func (s *SLOService) GetSLO(projectID, id int64) (*SLO, error) {
	slo, err := s.repo.GetSLO(projectID, id)
	if err != nil {
		return nil, s.fail("get slo failed", err, "project_id", projectID, "slo_id", id)
	}
	return slo, nil
}

// CreateSLO validates and stores an SLO.
func (s *SLOService) CreateSLO(projectID int64, in SLOInput) (int64, error) {
	slo, err := validateSLO(projectID, in)
	if err != nil {
		return 0, err
	}
	id, err := s.repo.CreateSLO(slo)
	if err != nil {
		return 0, s.fail("create slo failed", err, "project_id", projectID)
	}
	return id, nil
}

// UpdateSLO replaces the editable fields of an SLO of the project.
func (s *SLOService) UpdateSLO(projectID, id int64, in SLOInput) error {
	slo, err := validateSLO(projectID, in)
	if err != nil {
		return err
	}
	slo.ID = id
	if err := s.repo.UpdateSLO(slo); err != nil {
		return s.fail("update slo failed", err, "project_id", projectID, "slo_id", id)
	}
	return nil
}

// DeleteSLO removes an SLO of the project with its burn alerts and counts.
func (s *SLOService) DeleteSLO(projectID, id int64) error {
	if err := s.repo.DeleteSLO(projectID, id); err != nil {
		return s.fail("delete slo failed", err, "project_id", projectID, "slo_id", id)
	}
	return nil
}
