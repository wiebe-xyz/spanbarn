package alert

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var alertTracer = otel.Tracer("spanbarn/alert")

// AlertRepository defines the data access methods needed by the evaluator.
type AlertRepository interface {
	ListAlerts(projectID int64) ([]repository.Alert, error)
	QueryAggregates(filter repository.AggregateFilter) ([]repository.Aggregate, error)
	UpdateAlertLastTriggered(alertID int64, at time.Time) error
}

// Notifier defines how alert notifications are delivered.
type Notifier interface {
	SendWebhook(ctx context.Context, url string, payload AlertPayload) error
	SendEmail(ctx context.Context, to string, subject string, body string) error
}

// AlertPayload is the JSON payload sent in webhook notifications.
type AlertPayload struct {
	AlertID     int64     `json:"alertId"`
	Service     string    `json:"service"`
	Operation   string    `json:"operation"`
	Type        string    `json:"type"`
	Threshold   float64   `json:"threshold"`
	Current     float64   `json:"current"`
	Average     float64   `json:"average"`
	TriggeredAt time.Time `json:"triggeredAt"`
}

// Evaluator checks alert conditions against aggregate data and sends notifications.
type Evaluator struct {
	repo   AlertRepository
	notify Notifier
	logger *slog.Logger
	now    func() time.Time // injectable clock for testing
}

// NewEvaluator creates an Evaluator with the given dependencies.
func NewEvaluator(repo AlertRepository, notifier Notifier, logger *slog.Logger) *Evaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Evaluator{
		repo:   repo,
		notify: notifier,
		logger: logger,
		now:    time.Now,
	}
}

// Evaluate checks all enabled alerts for a project and sends notifications for triggered ones.
func (e *Evaluator) Evaluate(ctx context.Context, projectID int64) error {
	ctx, span := alertTracer.Start(ctx, "alert.evaluate_project")
	span.SetAttributes(attribute.Int64("project_id", projectID))
	defer span.End()

	alerts, err := e.repo.ListAlerts(projectID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("list alerts: %w", err)
	}

	span.SetAttributes(attribute.Int("alert_count", len(alerts)))
	now := e.now()

	var evaluated int
	for _, a := range alerts {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if !a.Enabled {
			continue
		}

		if a.LastTriggeredAt.Valid {
			cooldownEnd := a.LastTriggeredAt.Time.Add(time.Duration(a.CooldownMinutes) * time.Minute)
			if now.Before(cooldownEnd) {
				continue
			}
		}

		if err := e.evaluateAlert(ctx, a, now); err != nil {
			e.logger.Error("evaluate alert", "alertID", a.ID, "error", err)
		}
		evaluated++
	}

	span.SetAttributes(attribute.Int("evaluated_count", evaluated))
	return nil
}

func (e *Evaluator) evaluateAlert(ctx context.Context, a repository.Alert, now time.Time) error {
	_, span := alertTracer.Start(ctx, "alert.evaluate_single")
	span.SetAttributes(
		attribute.Int64("alert_id", a.ID),
		attribute.String("type", a.Type),
		attribute.String("service", a.Service),
		attribute.String("operation", a.Operation),
	)
	defer span.End()

	// Query the current (most recent) bucket aggregate.
	currentAggs, err := e.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: a.ProjectID,
		Service:   a.Service,
		Operation: a.Operation,
		To:        now,
		Limit:     1,
	})
	if err != nil {
		return fmt.Errorf("query current aggregate: %w", err)
	}
	if len(currentAggs) == 0 {
		return nil // no data yet
	}
	current := currentAggs[0]

	// Query previous N buckets for rolling average (skip the current one).
	historyAggs, err := e.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: a.ProjectID,
		Service:   a.Service,
		Operation: a.Operation,
		To:        current.Bucket.Add(-time.Second), // exclude current bucket
		Limit:     a.ComparisonWindow,
	})
	if err != nil {
		return fmt.Errorf("query history aggregates: %w", err)
	}

	// Compute current value and average based on alert type.
	var currentVal, avgVal float64

	switch a.Type {
	case "latency":
		currentVal = float64(current.P95Us) / 1000.0 // convert us to ms
		if len(historyAggs) > 0 {
			var sum int64
			for _, h := range historyAggs {
				sum += h.P95Us
			}
			avgVal = float64(sum) / float64(len(historyAggs)) / 1000.0 // us to ms
		}
	case "error_rate":
		if current.Count > 0 {
			currentVal = float64(current.ErrorCount) / float64(current.Count) * 100.0
		}
		if len(historyAggs) > 0 {
			var totalErrors, totalCount int64
			for _, h := range historyAggs {
				totalErrors += h.ErrorCount
				totalCount += h.Count
			}
			if totalCount > 0 {
				avgVal = float64(totalErrors) / float64(totalCount) * 100.0
			}
		}
	default:
		return fmt.Errorf("unknown alert type: %s", a.Type)
	}

	// Check if current exceeds threshold.
	if currentVal < a.Threshold {
		return nil
	}

	// Check if current is significantly higher than average (>20% increase).
	// If there's no history, we still alert if threshold is exceeded.
	if len(historyAggs) > 0 && avgVal > 0 {
		increase := (currentVal - avgVal) / avgVal
		if increase <= 0.20 {
			return nil
		}
	}

	span.AddEvent("alert.triggered", trace.WithAttributes(
		attribute.Float64("current", currentVal),
		attribute.Float64("average", avgVal),
		attribute.Float64("threshold", a.Threshold),
	))

	payload := AlertPayload{
		AlertID:     a.ID,
		Service:     a.Service,
		Operation:   a.Operation,
		Type:        a.Type,
		Threshold:   a.Threshold,
		Current:     currentVal,
		Average:     avgVal,
		TriggeredAt: now,
	}

	if a.WebhookURL != "" {
		if err := e.notify.SendWebhook(ctx, a.WebhookURL, payload); err != nil {
			e.logger.Error("send webhook", "alertID", a.ID, "error", err)
		}
	}

	if a.Email != "" {
		subject := fmt.Sprintf("[SpanBarn Alert] %s %s regression on %s", a.Type, a.Service, a.Operation)
		body := fmt.Sprintf(
			"Alert triggered for %s/%s\n\nType: %s\nCurrent: %.2f\nAverage: %.2f\nThreshold: %.2f\nTriggered at: %s",
			a.Service, a.Operation, a.Type, currentVal, avgVal, a.Threshold, now.Format(time.RFC3339),
		)
		if err := e.notify.SendEmail(ctx, a.Email, subject, body); err != nil {
			e.logger.Error("send email", "alertID", a.ID, "error", err)
		}
	}

	// Update last_triggered_at.
	if err := e.repo.UpdateAlertLastTriggered(a.ID, now); err != nil {
		return fmt.Errorf("update last_triggered_at: %w", err)
	}

	e.logger.Info("alert triggered",
		"alertID", a.ID,
		"type", a.Type,
		"service", a.Service,
		"operation", a.Operation,
		"current", currentVal,
		"average", avgVal,
		"threshold", a.Threshold,
	)

	return nil
}
