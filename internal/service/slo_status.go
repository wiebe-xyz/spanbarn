package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/slo"
)

// BurnAlertStatus is the state of one burn alert. CurrentBurn is the burn rate
// over the alert window. Firing is the state the evaluator last recorded.
type BurnAlertStatus struct {
	ID            int64   `json:"id"`
	WindowMinutes int     `json:"windowMinutes"`
	BurnRate      float64 `json:"burnRate"`
	Enabled       bool    `json:"enabled"`
	CurrentBurn   float64 `json:"currentBurn"`
	Good          int64   `json:"good"`
	Total         int64   `json:"total"`
	Firing        bool    `json:"firing"`
}

// SLOStatus is the error budget state of an SLO. With no traffic the budget is
// full and every burn is 0.
type SLOStatus struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Target          float64           `json:"target"`
	WindowDays      int               `json:"windowDays"`
	Good            int64             `json:"good"`
	Total           int64             `json:"total"`
	BudgetRemaining float64           `json:"budgetRemaining"`
	Alerts          []BurnAlertStatus `json:"alerts"`
}

// Status sums the stored counts over the SLO window and each alert window.
func (s *SLOService) Status(projectID, id int64) (*SLOStatus, error) {
	def, err := s.GetSLO(projectID, id)
	if err != nil {
		return nil, err
	}
	alerts, err := s.repo.ListSLOBurnAlerts(projectID, id)
	if err != nil {
		return nil, s.fail("slo status failed", err, "project_id", projectID, "slo_id", id)
	}
	// Buckets are keyed by their start, so the one in progress needs an end after now.
	to := s.now().UTC().Add(time.Second)
	good, total, err := s.repo.SumSLOCounts(id, to.Add(-time.Duration(def.WindowDays)*24*time.Hour), to)
	if err != nil {
		return nil, s.fail("slo status failed", err, "project_id", projectID, "slo_id", id)
	}
	st := &SLOStatus{
		ID: def.ID, Name: def.Name, Target: def.Target, WindowDays: def.WindowDays,
		Good: good, Total: total, BudgetRemaining: slo.BudgetRemaining(def.Target, good, total),
		Alerts: make([]BurnAlertStatus, 0, len(alerts)),
	}
	for _, a := range alerts {
		g, t, err := s.repo.SumSLOCounts(id, to.Add(-time.Duration(a.WindowMinutes)*time.Minute), to)
		if err != nil {
			return nil, s.fail("slo status failed", err, "project_id", projectID, "slo_id", id)
		}
		st.Alerts = append(st.Alerts, BurnAlertStatus{
			ID: a.ID, WindowMinutes: a.WindowMinutes, BurnRate: a.BurnRate, Enabled: a.Enabled,
			CurrentBurn: slo.BurnRate(def.Target, g, t), Good: g, Total: t, Firing: a.Firing,
		})
	}
	return st, nil
}
