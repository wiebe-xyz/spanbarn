package api

import (
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type burnAlertBody struct {
	WindowMinutes   int     `json:"windowMinutes"`
	BurnRate        float64 `json:"burnRate"`
	WebhookURL      string  `json:"webhookUrl"`
	Email           string  `json:"email"`
	CooldownMinutes int     `json:"cooldownMinutes"`
	Enabled         *bool   `json:"enabled"`
}

func (b burnAlertBody) input() service.BurnAlertInput {
	return service.BurnAlertInput{
		WindowMinutes: b.WindowMinutes, BurnRate: b.BurnRate, WebhookURL: b.WebhookURL,
		Email: b.Email, CooldownMinutes: b.CooldownMinutes, Enabled: b.Enabled,
	}
}

func (h *sloHandlers) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	alerts, err := h.svc.ListBurnAlerts(project, id)
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (h *sloHandlers) handleCreateAlert(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	var b burnAlertBody
	if !decodeBody(w, r, &b) {
		return
	}
	alertID, err := h.svc.CreateBurnAlert(project, id, b.input())
	if err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": alertID})
}

func (h *sloHandlers) handleUpdateAlert(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	alertID, ok := pathID(w, r, "alertId")
	if !ok {
		return
	}
	var b burnAlertBody
	if !decodeBody(w, r, &b) {
		return
	}
	if err := h.svc.UpdateBurnAlert(project, id, alertID, b.input()); err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *sloHandlers) handleDeleteAlert(w http.ResponseWriter, r *http.Request) {
	project, id, ok := sloRoute(w, r)
	if !ok {
		return
	}
	alertID, ok := pathID(w, r, "alertId")
	if !ok {
		return
	}
	if err := h.svc.DeleteBurnAlert(project, id, alertID); err != nil {
		writeSLOError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
