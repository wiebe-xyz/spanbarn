package service

import (
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// The configuration services add no behavior of their own. Errors pass
// through unchanged and the API layer decides the HTTP status; the HTTP
// error path logs them.

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
