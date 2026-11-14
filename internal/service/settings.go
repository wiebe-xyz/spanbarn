package service

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

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
