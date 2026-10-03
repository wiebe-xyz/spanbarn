package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The services in this file are the API layer's route to configuration data:
// alerts, saved queries, trace exclusions, settings, projects and exports.
// They add no behavior of their own. Errors pass through unchanged and the
// API layer decides the HTTP status; the HTTP error path logs them.

// AlertRepository is the storage AlertService needs.
type AlertRepository interface {
	ListAlerts(projectID int64) ([]repository.Alert, error)
	CreateAlert(a repository.Alert) (int64, error)
	UpdateAlert(a repository.Alert) error
	DeleteAlert(id int64) error
}

// AlertService manages alert rules.
type AlertService struct{ repo AlertRepository }

func NewAlertService(repo AlertRepository) *AlertService { return &AlertService{repo: repo} }

func (s *AlertService) List(projectID int64) ([]Alert, error) { return s.repo.ListAlerts(projectID) }
func (s *AlertService) Create(a Alert) (int64, error)         { return s.repo.CreateAlert(a) }
func (s *AlertService) Update(a Alert) error                  { return s.repo.UpdateAlert(a) }
func (s *AlertService) Delete(id int64) error                 { return s.repo.DeleteAlert(id) }

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

// SpanStreamRepository is the storage ExportService needs.
type SpanStreamRepository interface {
	StreamSpans(f repository.SpanFilter, fn func(repository.Span) error) error
}

// ExportService streams spans for download.
type ExportService struct{ repo SpanStreamRepository }

func NewExportService(repo SpanStreamRepository) *ExportService { return &ExportService{repo: repo} }

// StreamSpans calls fn for each span matching f until fn fails.
func (s *ExportService) StreamSpans(f SpanFilter, fn func(Span) error) error {
	return s.repo.StreamSpans(f, fn)
}

// SettingsRepository is the storage SettingsService needs.
type SettingsRepository interface {
	GetAllSettings() (map[string]string, error)
	SetSetting(key, value string) error
	DeleteSetting(key string) error
	GetDBSize(dbPath, spoolDir string) (*repository.DBSize, error)
	GetDBCounts() (*repository.DBCounts, error)
}

// SettingsService reads and writes runtime settings and storage statistics.
type SettingsService struct{ repo SettingsRepository }

func NewSettingsService(repo SettingsRepository) *SettingsService {
	return &SettingsService{repo: repo}
}

func (s *SettingsService) All() (map[string]string, error) { return s.repo.GetAllSettings() }
func (s *SettingsService) Set(key, value string) error     { return s.repo.SetSetting(key, value) }
func (s *SettingsService) Delete(key string) error         { return s.repo.DeleteSetting(key) }
func (s *SettingsService) DBSize(dbPath, spoolDir string) (*DBSize, error) {
	return s.repo.GetDBSize(dbPath, spoolDir)
}
func (s *SettingsService) DBCounts() (*DBCounts, error) { return s.repo.GetDBCounts() }

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
	ListAPIKeys(projectID int64) ([]repository.APIKey, error)
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
