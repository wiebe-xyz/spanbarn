package service

import (
	"net/mail"
	"net/url"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// BurnAlertInput is the editable part of a burn alert. A nil Enabled means on.
type BurnAlertInput struct {
	WindowMinutes   int
	BurnRate        float64
	WebhookURL      string
	Email           string
	CooldownMinutes int
	Enabled         *bool
}

// validateBurnAlert checks the input against the SLO window and returns the
// alert it describes.
func validateBurnAlert(slo *repository.SLO, in BurnAlertInput) (repository.SLOBurnAlert, error) {
	if in.WindowMinutes <= 0 {
		return repository.SLOBurnAlert{}, analyzeInvalid("windowMinutes must be above 0")
	}
	if limit := slo.WindowDays * 24 * 60; in.WindowMinutes >= limit {
		return repository.SLOBurnAlert{}, analyzeInvalid("windowMinutes must be shorter than the SLO window of %d minutes", limit)
	}
	if !(in.BurnRate > 0) {
		return repository.SLOBurnAlert{}, analyzeInvalid("burnRate must be above 0")
	}
	if in.CooldownMinutes < 0 {
		return repository.SLOBurnAlert{}, analyzeInvalid("cooldownMinutes must not be negative")
	}
	hook, email := strings.TrimSpace(in.WebhookURL), strings.TrimSpace(in.Email)
	if err := validateWebhook(hook); err != nil {
		return repository.SLOBurnAlert{}, err
	}
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			return repository.SLOBurnAlert{}, analyzeInvalid("email is not a valid address")
		}
	}
	cooldown := in.CooldownMinutes
	if cooldown == 0 {
		cooldown = DefaultBurnCooldownMinutes
	}
	return repository.SLOBurnAlert{
		SLOID: slo.ID, WindowMinutes: in.WindowMinutes, BurnRate: in.BurnRate,
		WebhookURL: hook, Email: email, CooldownMinutes: cooldown,
		Enabled: in.Enabled == nil || *in.Enabled,
	}, nil
}

func validateWebhook(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return analyzeInvalid("webhookUrl must be an http or https URL")
	}
	return nil
}

// ListBurnAlerts returns the burn alerts of an SLO of the project.
func (s *SLOService) ListBurnAlerts(projectID, sloID int64) ([]SLOBurnAlert, error) {
	if _, err := s.GetSLO(projectID, sloID); err != nil {
		return nil, err
	}
	alerts, err := s.repo.ListSLOBurnAlerts(projectID, sloID)
	if err != nil {
		return nil, s.fail("list slo burn alerts failed", err, "project_id", projectID, "slo_id", sloID)
	}
	if alerts == nil {
		alerts = []SLOBurnAlert{}
	}
	return alerts, nil
}

// CreateBurnAlert validates and adds a burn alert to an SLO of the project.
func (s *SLOService) CreateBurnAlert(projectID, sloID int64, in BurnAlertInput) (int64, error) {
	slo, err := s.GetSLO(projectID, sloID)
	if err != nil {
		return 0, err
	}
	alert, err := validateBurnAlert(slo, in)
	if err != nil {
		return 0, err
	}
	id, err := s.repo.CreateSLOBurnAlert(projectID, alert)
	if err != nil {
		return 0, s.fail("create slo burn alert failed", err, "project_id", projectID, "slo_id", sloID)
	}
	return id, nil
}

// burnAlertOf returns an alert only when it belongs to the SLO of the project.
func (s *SLOService) burnAlertOf(projectID, sloID, alertID int64) (*SLOBurnAlert, error) {
	alert, err := s.repo.GetSLOBurnAlert(projectID, alertID)
	if err != nil {
		return nil, s.fail("get slo burn alert failed", err, "project_id", projectID, "alert_id", alertID)
	}
	if alert.SLOID != sloID {
		return nil, ErrNotFound
	}
	return alert, nil
}

// UpdateBurnAlert replaces the editable fields of a burn alert of the SLO.
func (s *SLOService) UpdateBurnAlert(projectID, sloID, alertID int64, in BurnAlertInput) error {
	if _, err := s.burnAlertOf(projectID, sloID, alertID); err != nil {
		return err
	}
	slo, err := s.GetSLO(projectID, sloID)
	if err != nil {
		return err
	}
	alert, err := validateBurnAlert(slo, in)
	if err != nil {
		return err
	}
	alert.ID = alertID
	if err := s.repo.UpdateSLOBurnAlert(projectID, alert); err != nil {
		return s.fail("update slo burn alert failed", err, "project_id", projectID, "alert_id", alertID)
	}
	return nil
}

// DeleteBurnAlert removes a burn alert of the SLO.
func (s *SLOService) DeleteBurnAlert(projectID, sloID, alertID int64) error {
	if _, err := s.burnAlertOf(projectID, sloID, alertID); err != nil {
		return err
	}
	if err := s.repo.DeleteSLOBurnAlert(projectID, alertID); err != nil {
		return s.fail("delete slo burn alert failed", err, "project_id", projectID, "alert_id", alertID)
	}
	return nil
}
