package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/slo"
)

// SLOReader is the read side of the SLO evaluator. It can run on a read-only
// connection: counting scans span attributes and should not contend with the
// writer.
type SLOReader interface {
	ListSLOs(projectID int64) ([]repository.SLO, error)
	ListSLOBurnAlerts(projectID, sloID int64) ([]repository.SLOBurnAlert, error)
	CountSpans(f repository.SpanFilter) (total, errors int64, err error)
	LatestSLOBucket(sloID int64) (time.Time, error)
	SumSLOCounts(sloID int64, from, to time.Time) (good, total int64, err error)
}

// SLOWriter is the write side: stored counts, burn alert state and pruning.
type SLOWriter interface {
	InsertSLOCounts(counts []repository.SLOCount) error
	UpdateSLOBurnAlertState(id int64, firing bool, at time.Time) error
	DeleteSLOCountsBefore(cutoff time.Time) (int64, error)
}

// SLORepository is the full data access of the SLO evaluator.
type SLORepository interface {
	SLOReader
	SLOWriter
}

// SLONotifier delivers burn alert notifications. *DefaultNotifier satisfies it.
type SLONotifier interface {
	SendSLOWebhook(ctx context.Context, url string, payload SLOPayload) error
	SendEmail(ctx context.Context, to string, subject string, body string) error
}

// SLOPayload is the JSON payload sent in burn alert webhooks.
type SLOPayload struct {
	SLOID           int64     `json:"sloId"`
	AlertID         int64     `json:"alertId"`
	Name            string    `json:"name"`
	WindowMinutes   int       `json:"windowMinutes"`
	BurnRate        float64   `json:"burnRate"`
	Threshold       float64   `json:"threshold"`
	BudgetRemaining float64   `json:"budgetRemaining"`
	TriggeredAt     time.Time `json:"triggeredAt"`
}

// SLOEvaluator counts good and total spans per SLO and fires burn alerts.
type SLOEvaluator struct {
	reads       SLOReader
	writes      SLOWriter
	notify      SLONotifier
	logger      *slog.Logger
	now         func() time.Time // injectable clock for testing
	ratioLookup SampleRatioLookup
}

// NewSLOEvaluator creates an SLOEvaluator. reads and writes may be the same
// repository. Pass a nil ratioLookup for no sampling correction.
func NewSLOEvaluator(reads SLOReader, writes SLOWriter, notifier SLONotifier, logger *slog.Logger, ratioLookup SampleRatioLookup) *SLOEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &SLOEvaluator{
		reads:       reads,
		writes:      writes,
		notify:      notifier,
		logger:      logger,
		now:         time.Now,
		ratioLookup: ratioLookup,
	}
}

// Run counts and evaluates the SLOs of the given projects, then prunes counts
// no window needs any more. One failing SLO or project never stops the rest.
func (e *SLOEvaluator) Run(ctx context.Context, projectIDs []int64) {
	ctx, span := alertTracer.Start(ctx, "alert.slo_run")
	defer span.End()

	now := e.now()
	closeBefore := sloCloseBefore(now)
	var keep time.Duration
	complete := true
	for _, id := range projectIDs {
		if ctx.Err() != nil {
			return
		}
		k, err := e.evaluateProject(ctx, id, now, closeBefore)
		if err != nil {
			complete = false
			e.logger.Error("evaluate slos", "projectID", id, "error", err)
		}
		keep = max(keep, k)
	}
	// Pruning uses one cutoff for every SLO, so it only runs when every project
	// reported its window; otherwise a longer window could lose its history.
	if complete && keep > 0 {
		e.prune(now.Add(-keep))
	}
}

func (e *SLOEvaluator) prune(cutoff time.Time) {
	if _, err := e.writes.DeleteSLOCountsBefore(cutoff); err != nil {
		e.logger.Error("prune slo counts", "error", err)
	}
}

// evaluateProject returns how long the project's counts must be kept. The error
// is set when the project's SLO list could not be read.
func (e *SLOEvaluator) evaluateProject(ctx context.Context, projectID int64, now, closeBefore time.Time) (time.Duration, error) {
	ctx, span := alertTracer.Start(ctx, "alert.slo_evaluate_project")
	span.SetAttributes(attribute.Int64("project_id", projectID))
	defer span.End()

	slos, err := e.reads.ListSLOs(projectID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, fmt.Errorf("list slos: %w", err)
	}
	span.SetAttributes(attribute.Int("slo_count", len(slos)))

	var keep time.Duration
	for _, s := range slos {
		k, err := e.evaluateSLO(ctx, s, now, closeBefore)
		if err != nil {
			e.logger.Error("evaluate slo", "sloID", s.ID, "projectID", projectID, "error", err)
		}
		keep = max(keep, k)
	}
	return keep, nil
}

// evaluateSLO counts the SLO and evaluates its burn alerts. When counting
// fails the alerts are skipped: they would read a series with a hole at its
// newest end and could clear a firing alert on missing data.
func (e *SLOEvaluator) evaluateSLO(ctx context.Context, s repository.SLO, now, closeBefore time.Time) (time.Duration, error) {
	keep := time.Duration(s.WindowDays) * 24 * time.Hour

	alerts, listErr := e.reads.ListSLOBurnAlerts(s.ProjectID, s.ID)
	for _, a := range alerts {
		keep = max(keep, time.Duration(a.WindowMinutes)*time.Minute)
	}

	if err := e.countSLO(ctx, s, closeBefore); err != nil {
		return keep, fmt.Errorf("count: %w", err)
	}
	if listErr != nil {
		return keep, fmt.Errorf("list burn alerts: %w", listErr)
	}

	var errs []error
	for _, a := range alerts {
		if ctx.Err() != nil {
			return keep, ctx.Err()
		}
		if !a.Enabled {
			continue
		}
		if err := e.evaluateBurnAlert(ctx, s, a, now, closeBefore); err != nil {
			errs = append(errs, fmt.Errorf("burn alert %d: %w", a.ID, err))
		}
	}
	return keep, errors.Join(errs...)
}

// evaluateBurnAlert fires an alert whose burn rate over its window exceeds the
// threshold, clears the firing flag once it no longer does, and does nothing
// while the state already matches. A firing alert notifies once; the cooldown
// holds back a new notification when the burn crosses again shortly after a
// recovery.
func (e *SLOEvaluator) evaluateBurnAlert(ctx context.Context, s repository.SLO, a repository.SLOBurnAlert, now, closeBefore time.Time) (err error) {
	ctx, span := alertTracer.Start(ctx, "alert.slo_burn_alert")
	span.SetAttributes(attribute.Int64("slo_id", s.ID), attribute.Int64("alert_id", a.ID))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	if a.WindowMinutes <= 0 {
		return fmt.Errorf("window_minutes %d", a.WindowMinutes)
	}
	from := closeBefore.Add(-time.Duration(a.WindowMinutes) * time.Minute)
	good, total, err := e.reads.SumSLOCounts(s.ID, from, closeBefore)
	if err != nil {
		return fmt.Errorf("sum counts: %w", err)
	}
	burn := slo.BurnRate(s.Target, good, total)
	span.SetAttributes(attribute.Float64("burn_rate", burn), attribute.Bool("firing", a.Firing))

	exceeds := burn > a.BurnRate
	switch {
	case exceeds && !a.Firing:
		if inCooldown(a, now) {
			return nil
		}
		return e.fire(ctx, s, a, burn, now, closeBefore)
	case !exceeds && a.Firing:
		if err := e.writes.UpdateSLOBurnAlertState(a.ID, false, time.Time{}); err != nil {
			return fmt.Errorf("clear firing: %w", err)
		}
		e.logger.Info("slo burn alert recovered", "alertID", a.ID, "sloID", s.ID, "burnRate", burn)
	}
	return nil
}

func inCooldown(a repository.SLOBurnAlert, now time.Time) bool {
	if !a.LastTriggeredAt.Valid {
		return false
	}
	return now.Before(a.LastTriggeredAt.Time.Add(time.Duration(a.CooldownMinutes) * time.Minute))
}
