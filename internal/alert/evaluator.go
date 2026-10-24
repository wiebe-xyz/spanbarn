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

// AlertRepository defines the read data access methods needed by the evaluator.
// These run every interval and can be pointed at a read-only DB connection so
// alert evaluation never contends with the single writer connection.
type AlertRepository interface {
	ListAlerts(projectID int64) ([]repository.Alert, error)
	QueryAggregates(filter repository.AggregateFilter) ([]repository.Aggregate, error)
	QueryMetricRollups(ctx context.Context, f repository.MetricRollupFilter) ([]repository.MetricRollup, error)
	UpdateAlertLastTriggered(alertID int64, at time.Time) error
}

// AlertTriggerWriter performs the one write the evaluator makes — recording a
// trigger. It only fires when an alert actually crosses its threshold (rare), so
// it can stay on the writable connection while reads move to the read-only one.
type AlertTriggerWriter interface {
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

// SampleRatioLookup returns the 1-in-N sampling ratio for a project.
// Satisfied by *ingest.CachedRatioLookup and *ingest.StaticRatioLookup.
type SampleRatioLookup interface {
	Ratio(ctx context.Context, projectID int64, operation string) int
}

// Evaluator checks alert conditions against aggregate data and sends notifications.
type Evaluator struct {
	repo        AlertRepository    // reads (may be a read-only connection)
	triggers    AlertTriggerWriter // the rare trigger write (writable connection)
	notify      Notifier
	logger      *slog.Logger
	now         func() time.Time // injectable clock for testing
	ratioLookup SampleRatioLookup
	failures    evalFailures
	aggInterval time.Duration // width of one aggregate bucket
}

// NewEvaluator creates an Evaluator with the given dependencies. ratioLookup
// is used to correct error rates for projects using error-biased sampling.
// Pass nil for no correction. repo serves both reads and (by default) the
// trigger write; call SetTriggerWriter to route the write to a separate
// (writable) connection when repo is read-only.
func NewEvaluator(repo AlertRepository, notifier Notifier, logger *slog.Logger, ratioLookup SampleRatioLookup) *Evaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Evaluator{
		repo:        repo,
		triggers:    repo,
		notify:      notifier,
		logger:      logger,
		now:         time.Now,
		ratioLookup: ratioLookup,
		aggInterval: time.Minute,
	}
}

// SetAggregateInterval sets the width of one aggregate bucket, which sizes how
// far back each alert reads. Non-positive values are ignored.
func (e *Evaluator) SetAggregateInterval(d time.Duration) {
	if d > 0 {
		e.aggInterval = d
	}
}

// SetTriggerWriter routes the trigger-recording write to a separate connection
// (typically the writable one) so read queries can run on a read-only pool.
func (e *Evaluator) SetTriggerWriter(w AlertTriggerWriter) {
	if w != nil {
		e.triggers = w
	}
}

func (e *Evaluator) projectSampleRate(ctx context.Context, projectID int64) float64 {
	return sampleRateFor(ctx, e.ratioLookup, projectID)
}

// sampleRateFor returns the kept share (1/N) of a project's ok spans.
func sampleRateFor(ctx context.Context, lookup SampleRatioLookup, projectID int64) float64 {
	if lookup == nil {
		return 1.0
	}
	ratio := lookup.Ratio(ctx, projectID, "")
	if ratio <= 1 {
		return 1.0
	}
	return 1.0 / float64(ratio)
}

// inflateCount mirrors service.inflateCount: scales ok-span counts by 1/sampleRate.
func inflateCount(count, errorCount int64, sampleRate float64) int64 {
	if sampleRate >= 1.0 {
		return count
	}
	okCount := count - errorCount
	if okCount < 0 {
		okCount = 0
	}
	return errorCount + int64(float64(okCount)/sampleRate)
}

// estimateCount mirrors service.estimateCount: the summed sample weights when
// every span of the bucket carried one, otherwise inflateCount.
func estimateCount(a repository.Aggregate, sampleRate float64) int64 {
	if n, ok := a.WeightedEstimate(); ok {
		return n
	}
	return inflateCount(a.Count, a.ErrorCount, sampleRate)
}

// estimateSpans mirrors service.estimateSpans: the summed sample weights of
// spans read from the spans table when every one carried a weight, otherwise
// inflateCount.
func estimateSpans(c repository.SpanCount, sampleRate float64) int64 {
	if n, ok := c.Weights.Estimate(c.Total); ok {
		return n
	}
	return inflateCount(c.Total, c.Errors, sampleRate)
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

		err := e.evaluateAlert(ctx, a, now)
		if e.failures.record(a.ID, err) {
			e.logger.Error("evaluate alert", "alertID", a.ID, "error", err)
		} else if err != nil {
			e.logger.Warn("evaluate alert timed out, retrying next tick", "alertID", a.ID, "error", err)
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

	if a.Type == "metric_threshold" {
		return e.evaluateMetricAlert(ctx, a, now)
	}

	buckets, err := e.serviceBuckets(a, now)
	if err != nil {
		return fmt.Errorf("query current aggregate: %w", err)
	}
	if len(buckets) == 0 {
		return nil // no data yet
	}
	current := buckets[0]
	historyAggs := buckets[1:]
	if len(historyAggs) > a.ComparisonWindow {
		historyAggs = historyAggs[:a.ComparisonWindow]
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
		sr := e.projectSampleRate(ctx, a.ProjectID)
		if eff := estimateCount(current, sr); eff > 0 {
			currentVal = float64(current.ErrorCount) / float64(eff) * 100.0
		}
		if len(historyAggs) > 0 {
			var totalErrors, totalCount int64
			for _, h := range historyAggs {
				totalErrors += h.ErrorCount
				totalCount += estimateCount(h, sr)
			}
			if totalCount > 0 {
				avgVal = float64(totalErrors) / float64(totalCount) * 100.0
			}
		}
	default:
		return fmt.Errorf("unknown alert type: %s", a.Type)
	}

	return e.maybeTrigger(ctx, span, a, currentVal, avgVal, len(historyAggs) > 0, now)
}

// aggregateLookback bounds how far back the evaluator reads aggregate rows for
// the current bucket and its history. aggregateRowCap bounds how many rows that
// read returns; rows come newest first, so a cap only trims the oldest history.
const (
	aggregateLookback = 24 * time.Hour
	aggregateRowCap   = 50000
)

// Lookback sizing: a rule reads its current bucket plus ComparisonWindow
// history buckets, so it needs ComparisonWindow+1 buckets of the aggregate
// interval, not a day. The floor leaves room for gaps; aggregateLookback stays
// the ceiling.
const minLookback = time.Hour

// lookbackFor returns how far back an alert with the given comparison window
// reads aggregate rows when one bucket spans interval.
func lookbackFor(comparisonWindow int, interval time.Duration) time.Duration {
	if comparisonWindow < 0 {
		comparisonWindow = 0
	}
	d := time.Duration(comparisonWindow+1) * interval
	if d < minLookback {
		return minLookback
	}
	if d > aggregateLookback {
		return aggregateLookback
	}
	return d
}

// serviceBuckets returns the alert's aggregates summed per bucket, newest first.
// A bucket holds one row per (operation, resource, kind), so an alert without
// an operation, or an operation seen under several kinds, has many rows per
// bucket. Taking rows one by one compared one operation's latest bucket with
// other operations' rows of the same bucket. Counts and errors add up; p95 is
// the count-weighted mean of the rows' p95, which is close but not exact.
func (e *Evaluator) serviceBuckets(a repository.Alert, now time.Time) ([]repository.Aggregate, error) {
	rows, err := e.repo.QueryAggregates(repository.AggregateFilter{
		ProjectID: a.ProjectID,
		Service:   a.Service,
		Operation: a.Operation,
		From:      now.Add(-lookbackFor(a.ComparisonWindow, e.aggInterval)),
		To:        now,
		Limit:     aggregateRowCap,
	})
	if err != nil {
		return nil, err
	}
	return sumByBucket(rows), nil
}

// sumByBucket folds rows ordered newest first into one aggregate per bucket,
// keeping that order.
func sumByBucket(rows []repository.Aggregate) []repository.Aggregate {
	var out []repository.Aggregate
	var weighted []float64
	for _, r := range rows {
		n := len(out)
		if n == 0 || !out[n-1].Bucket.Equal(r.Bucket) {
			out = append(out, repository.Aggregate{
				ProjectID: r.ProjectID, Service: r.Service, Bucket: r.Bucket,
			})
			weighted = append(weighted, 0)
			n++
		}
		b := &out[n-1]
		b.Count += r.Count
		b.ErrorCount += r.ErrorCount
		b.WeightedCount += r.WeightedCount
		b.WeightedSpans += r.WeightedSpans
		weighted[n-1] += float64(r.P95Us) * float64(r.Count)
	}
	for i := range out {
		if out[i].Count > 0 {
			out[i].P95Us = int64(weighted[i] / float64(out[i].Count))
		}
	}
	return out
}

// maybeTrigger applies the shared threshold + significant-increase gate and, if
// crossed, sends notifications and records the trigger time. hasHistory tells it
// whether a rolling baseline was available for the >20% increase check.
func (e *Evaluator) maybeTrigger(ctx context.Context, span trace.Span, a repository.Alert, currentVal, avgVal float64, hasHistory bool, now time.Time) error {
	// Check if current exceeds threshold.
	if currentVal < a.Threshold {
		return nil
	}

	// Check if current is significantly higher than average (>20% increase).
	// If there's no history, we still alert if threshold is exceeded.
	if hasHistory && avgVal > 0 {
		increase := (currentVal - avgVal) / avgVal
		if increase <= 0.20 {
			return nil
		}
	}

	target := a.Service
	if a.Type == "metric_threshold" {
		target = a.MetricName
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
		subject := fmt.Sprintf("[SpanBarn Alert] %s regression on %s", a.Type, target)
		body := fmt.Sprintf(
			"Alert triggered for %s\n\nType: %s\nCurrent: %.2f\nAverage: %.2f\nThreshold: %.2f\nTriggered at: %s",
			target, a.Type, currentVal, avgVal, a.Threshold, now.Format(time.RFC3339),
		)
		if err := e.notify.SendEmail(ctx, a.Email, subject, body); err != nil {
			e.logger.Error("send email", "alertID", a.ID, "error", err)
		}
	}

	// Update last_triggered_at on the writable connection.
	if err := e.triggers.UpdateAlertLastTriggered(a.ID, now); err != nil {
		return fmt.Errorf("update last_triggered_at: %w", err)
	}

	e.logger.Info("alert triggered",
		"alertID", a.ID,
		"type", a.Type,
		"target", target,
		"current", currentVal,
		"average", avgVal,
		"threshold", a.Threshold,
	)

	return nil
}
