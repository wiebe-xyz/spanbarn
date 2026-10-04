package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/slo"
)

// fire notifies through the alert's channels and records the alert as firing.
// When every configured channel fails the state stays untouched, so the next
// tick tries again. A channel that fails while another succeeds is logged and
// does not hold the state back, which would resend to the working one.
func (e *SLOEvaluator) fire(ctx context.Context, s repository.SLO, a repository.SLOBurnAlert, burn float64, now, closeBefore time.Time) error {
	from := closeBefore.Add(-time.Duration(s.WindowDays) * 24 * time.Hour)
	good, total, err := e.reads.SumSLOCounts(s.ID, from, closeBefore)
	if err != nil {
		return fmt.Errorf("sum budget counts: %w", err)
	}
	payload := SLOPayload{
		SLOID:           s.ID,
		AlertID:         a.ID,
		Name:            s.Name,
		WindowMinutes:   a.WindowMinutes,
		BurnRate:        burn,
		Threshold:       a.BurnRate,
		BudgetRemaining: slo.BudgetRemaining(s.Target, good, total),
		TriggeredAt:     now,
	}

	if err := e.deliver(ctx, a, payload); err != nil {
		return err
	}
	if err := e.writes.UpdateSLOBurnAlertState(a.ID, true, now); err != nil {
		return fmt.Errorf("record firing: %w", err)
	}
	e.logger.Info("slo burn alert fired",
		"alertID", a.ID, "sloID", s.ID, "burnRate", burn, "threshold", a.BurnRate,
		"budgetRemaining", payload.BudgetRemaining)
	return nil
}

// deliver sends the webhook and email of an alert. It returns an error only
// when at least one channel is configured and none of them succeeded.
func (e *SLOEvaluator) deliver(ctx context.Context, a repository.SLOBurnAlert, p SLOPayload) error {
	var sent int
	var errs []error
	if a.WebhookURL != "" {
		if err := e.notify.SendSLOWebhook(ctx, a.WebhookURL, p); err != nil {
			e.logger.Error("send slo webhook", "alertID", a.ID, "error", err)
			errs = append(errs, fmt.Errorf("webhook: %w", err))
		} else {
			sent++
		}
	}
	if a.Email != "" {
		subject := fmt.Sprintf("[SpanBarn SLO] %s is burning its error budget", p.Name)
		if err := e.notify.SendEmail(ctx, a.Email, subject, sloEmailBody(p)); err != nil {
			e.logger.Error("send slo email", "alertID", a.ID, "error", err)
			errs = append(errs, fmt.Errorf("email: %w", err))
		} else {
			sent++
		}
	}
	if sent == 0 && len(errs) > 0 {
		return fmt.Errorf("notify: %w", errors.Join(errs...))
	}
	return nil
}

func sloEmailBody(p SLOPayload) string {
	return fmt.Sprintf(
		"SLO burn alert for %s\n\nBurn rate over %d minutes: %.2f\nThreshold: %.2f\nBudget remaining: %.1f%%\nTriggered at: %s",
		p.Name, p.WindowMinutes, p.BurnRate, p.Threshold, p.BudgetRemaining*100, p.TriggeredAt.Format(time.RFC3339),
	)
}
