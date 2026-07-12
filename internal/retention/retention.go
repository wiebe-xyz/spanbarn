package retention

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Aggregator groups raw spans into aggregates and persists them.
// Satisfied by both *aggregation.Aggregator and *aggregation.Accumulator.
type Aggregator interface {
	AggregateSpans(ctx context.Context, spans []repository.Span) ([]repository.Aggregate, error)
	Persist(ctx context.Context, aggregates []repository.Aggregate) error
}

var tracer = otel.Tracer("spanbarn/retention")

const (
	defaultBatchSize = 5000
	// maxSpansPerCycle caps how many spans each RunOnce call will aggregate and
	// delete. Keeping cycles short prevents long write-lock holds that starve
	// the span-insert path. Any backlog beyond this cap is picked up on the
	// next tick.
	maxSpansPerCycle = 50_000
	// largeBacklogWarn is the span count above which we emit a warning so
	// operators know the table has grown unexpectedly large.
	largeBacklogWarn = 1_000_000
)

// Repository defines the data-access methods the retention worker needs.
type Repository interface {
	CountSpansOlderThan(cutoff time.Time) (int64, error)
	GetSpansForAggregation(cutoff time.Time, limit int) ([]repository.Span, error)
	DeleteSpansByMaxID(maxID int64) (int64, error)
	DeleteSpansOlderThan(cutoff time.Time) (int64, error)
	DeleteExpiredBoringSpans(ctx context.Context, now time.Time) (int64, error)
	InsertErrorSamples(spans []repository.Span) error
	DeleteErrorSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteAggregatesOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredE2EUsers(now time.Time) (int64, error)
	DeleteExpiredWebSessions(now time.Time) (int64, error)
	DeleteMetricsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteMetricRollupsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteLogsOlderThan(ctx context.Context, cutoff, errorLogCutoff time.Time) (int64, error)
	GetSetting(key string) (string, error)
}

// Config controls the retention worker's behaviour.
type Config struct {
	FullRetentionHours        int           // hours to keep ALL spans (default 4)
	InterestingRetentionHours int           // hours to keep error/slow spans after full retention expires (default 168 = 7d)
	BoringRetentionMinutes    int           // minutes to keep sampled boring spans (default 30); 0 disables boring cleanup
	ErrorRetentionDays        int           // days to keep error samples (default 30)
	AggregateRetentionDays    int           // days to keep aggregates (default 365)
	MetricsRetentionDays      int           // days to keep raw metric data points (default 90)
	MetricRollupRetentionDays int           // days to keep downsampled metric rollups (default 365)
	LogRetentionHours         int           // hours to keep log records (default 24)
	ErrorLogRetentionDays     int           // days to keep logs for error-sampled traces (default 30)
	SlowThresholdUS           int64         // microseconds above which a span is "slow"
	Interval                  time.Duration // how often to run (default 5m)
	// BatchYield is how long the worker voluntarily releases the write lock
	// between deletion batches so the span-insert worker can drain the Redis
	// queue. 0 disables yielding (old behaviour). Default 30s.
	BatchYield time.Duration
}

func (c Config) withDefaults() Config {
	if c.FullRetentionHours <= 0 {
		c.FullRetentionHours = 4
	}
	if c.InterestingRetentionHours <= 0 {
		c.InterestingRetentionHours = 168
	}
	if c.BoringRetentionMinutes <= 0 {
		c.BoringRetentionMinutes = 30
	}
	if c.ErrorRetentionDays <= 0 {
		c.ErrorRetentionDays = 30
	}
	if c.AggregateRetentionDays <= 0 {
		c.AggregateRetentionDays = 365
	}
	if c.MetricsRetentionDays <= 0 {
		c.MetricsRetentionDays = 90
	}
	if c.MetricRollupRetentionDays <= 0 {
		c.MetricRollupRetentionDays = 365
	}
	if c.LogRetentionHours <= 0 {
		c.LogRetentionHours = 24
	}
	if c.ErrorLogRetentionDays <= 0 {
		c.ErrorLogRetentionDays = 30
	}
	if c.SlowThresholdUS <= 0 {
		c.SlowThresholdUS = 1_000_000 // 1 second
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.BatchYield == 0 {
		c.BatchYield = 30 * time.Second
	}
	return c
}

// RetentionWorker manages span lifecycle: aggregate old spans, sample
// errors/slow spans, and delete data past its retention window.
type RetentionWorker struct {
	repo       Repository
	aggregator Aggregator
	cfg        Config
	logger     *slog.Logger
}

// NewRetentionWorker creates a new retention worker.
func NewRetentionWorker(repo Repository, aggregator Aggregator, cfg Config, logger *slog.Logger) *RetentionWorker {
	return &RetentionWorker{
		repo:       repo,
		aggregator: aggregator,
		cfg:        cfg.withDefaults(),
		logger:     logger,
	}
}

// Run starts the retention loop, ticking at cfg.Interval until ctx is cancelled.
func (w *RetentionWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("retention worker stopped")
			return
		case <-ticker.C:
			var lastErr error
			for attempt := 1; attempt <= 5; attempt++ {
				if lastErr = w.RunOnce(ctx); lastErr == nil {
					break
				}
				w.logger.Info("retention cycle attempt failed", "attempt", attempt, "error", lastErr)
				backoff := time.Duration(attempt*attempt) * time.Second
				time.Sleep(backoff)
			}
			if lastErr != nil {
				w.logger.Info("retention cycle failed, will retry next tick", "error", lastErr)
			}
		}
	}
}

// effectiveConfig returns the retention config, overriding with DB settings where present.
func (w *RetentionWorker) effectiveConfig() Config {
	cfg := w.cfg
	readInt := func(key string) (int, bool) {
		v, err := w.repo.GetSetting(key)
		if err != nil || v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, false
		}
		return n, true
	}
	if n, ok := readInt("retention_full_hours"); ok {
		cfg.FullRetentionHours = n
	}
	if n, ok := readInt("retention_interesting_hours"); ok {
		cfg.InterestingRetentionHours = n
	}
	if n, ok := readInt("retention_aggregated_days"); ok {
		cfg.AggregateRetentionDays = n
	}
	if n, ok := readInt("retention_error_days"); ok {
		cfg.ErrorRetentionDays = n
	}
	if n, ok := readInt("boring_retention_minutes"); ok {
		cfg.BoringRetentionMinutes = n
	}
	if n, ok := readInt("metrics_retention_days"); ok {
		cfg.MetricsRetentionDays = n
	}
	if n, ok := readInt("log_retention_hours"); ok {
		cfg.LogRetentionHours = n
	}
	if n, ok := readInt("error_log_retention_days"); ok {
		cfg.ErrorLogRetentionDays = n
	}
	return cfg
}

// RunOnce executes a single retention cycle:
//  1. Fetch spans older than full_retention_hours in batches, sample errors, aggregate, delete
//  2. Delete old error_samples and aggregates
//
// Each cycle is capped at maxSpansPerCycle deletions to keep write-lock hold
// times short. If the cap is reached, backlog_remains is logged so operators
// know the next tick will continue draining.
// lockBatch runs fn then sleeps for yield, giving the write scheduler a window
// to drain high-priority writes between retention deletion batches.
func (w *RetentionWorker) lockBatch(ctx context.Context, yield time.Duration, fn func()) {
	fn()
	if yield > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(yield):
		}
	}
}

func (w *RetentionWorker) RunOnce(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "retention.cycle")
	defer span.End()

	cfg := w.effectiveConfig()

	now := time.Now().UTC()
	interestingCutoff := now.Add(-time.Duration(cfg.InterestingRetentionHours) * time.Hour)
	errorCutoff := now.Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour)
	aggCutoff := now.Add(-time.Duration(cfg.AggregateRetentionDays) * 24 * time.Hour)
	metricsCutoff := now.Add(-time.Duration(cfg.MetricsRetentionDays) * 24 * time.Hour)
	metricRollupCutoff := now.Add(-time.Duration(cfg.MetricRollupRetentionDays) * 24 * time.Hour)
	logCutoff := now.Add(-time.Duration(cfg.LogRetentionHours) * time.Hour)
	errorLogCutoff := now.Add(-time.Duration(cfg.ErrorLogRetentionDays) * 24 * time.Hour)

	// Count spans pending deletion and warn if the backlog is unexpectedly large.
	if pending, err := w.repo.CountSpansOlderThan(interestingCutoff); err != nil {
		w.logger.Warn("retention: count pending spans failed", "error", err)
	} else {
		w.logger.Info("retention: pending spans", "count", pending)
		if pending > largeBacklogWarn {
			w.logger.Warn("retention: large backlog detected, drain will span multiple cycles",
				"pending_spans", pending, "per_cycle_cap", maxSpansPerCycle)
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Aggregate-then-delete all old spans (error/slow included),
	// capped at maxSpansPerCycle total to bound write-lock hold time.
	// The write mutex is held only for the duration of one batch, then released
	// for cfg.BatchYield so the span-insert worker can drain the Redis queue.
	var totalAggregated, totalSampled, spansDeleted int64
	var backlogRemains bool
	var batchErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batch, err := w.repo.GetSpansForAggregation(interestingCutoff, defaultBatchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}

		w.lockBatch(ctx, cfg.BatchYield, func() {
			var samples []repository.Span
			for _, s := range batch {
				if s.Status == "error" || s.DurationUs > cfg.SlowThresholdUS {
					samples = append(samples, s)
				}
			}
			if len(samples) > 0 {
				if err := w.repo.InsertErrorSamples(samples); err != nil {
					batchErr = err
					return
				}
				totalSampled += int64(len(samples))
			}

			aggs, err := w.aggregator.AggregateSpans(ctx, batch)
			if err != nil {
				batchErr = err
				return
			}
			if err := w.aggregator.Persist(ctx, aggs); err != nil {
				batchErr = err
				return
			}
			totalAggregated += int64(len(batch))

			maxID := batch[0].ID
			for _, s := range batch[1:] {
				if s.ID > maxID {
					maxID = s.ID
				}
			}
			deleted, err := w.repo.DeleteSpansByMaxID(maxID)
			if err != nil {
				batchErr = err
				return
			}
			spansDeleted += deleted
			w.logger.Info("retention: batch deleted", "deleted", deleted, "cycle_total", spansDeleted)
		})

		if batchErr != nil {
			span.RecordError(batchErr)
			span.SetStatus(codes.Error, batchErr.Error())
			return batchErr
		}
		if len(batch) < defaultBatchSize {
			break
		}
		if spansDeleted >= maxSpansPerCycle {
			backlogRemains = true
			break
		}
	}

	// Fast boring-span cleanup: delete sampled-boring spans whose stamped
	// expires_at has passed. Classification writes expires_at (= ingested_at +
	// boring retention) at storage time, so this is a bounded seek of the partial
	// idx_spans_expires index — it no longer scans the table classifying by
	// status/duration_us (which had wedged the writer for 30s+).
	boringDeleted, err := w.repo.DeleteExpiredBoringSpans(ctx, now)
	if err != nil {
		return err
	}

	errorSamplesDeleted, err := w.repo.DeleteErrorSamplesOlderThan(ctx, errorCutoff)
	if err != nil {
		return err
	}
	aggregatesDeleted, err := w.repo.DeleteAggregatesOlderThan(ctx, aggCutoff)
	if err != nil {
		return err
	}
	metricsDeleted, err := w.repo.DeleteMetricsOlderThan(ctx, metricsCutoff)
	if err != nil {
		return err
	}
	metricRollupsDeleted, err := w.repo.DeleteMetricRollupsOlderThan(ctx, metricRollupCutoff)
	if err != nil {
		return err
	}
	logsDeleted, err := w.repo.DeleteLogsOlderThan(ctx, logCutoff, errorLogCutoff)
	if err != nil {
		return err
	}
	e2eUsersDeleted, err := w.repo.DeleteExpiredE2EUsers(now)
	if err != nil {
		return err
	}
	// Web sessions past their absolute cap are already unusable (the session
	// middleware enforces absolute_expires_at); this prunes the rows.
	webSessionsDeleted, err := w.repo.DeleteExpiredWebSessions(now)
	if err != nil {
		return err
	}

	span.SetAttributes(
		attribute.Int64("spans_aggregated", totalAggregated),
		attribute.Int64("errors_sampled", totalSampled),
		attribute.Int64("spans_deleted", spansDeleted),
		attribute.Int64("boring_deleted", boringDeleted),
		attribute.Int64("error_samples_deleted", errorSamplesDeleted),
		attribute.Int64("aggregates_deleted", aggregatesDeleted),
		attribute.Int64("metrics_deleted", metricsDeleted),
		attribute.Int64("metric_rollups_deleted", metricRollupsDeleted),
		attribute.Int64("logs_deleted", logsDeleted),
		attribute.Int64("e2e_users_deleted", e2eUsersDeleted),
		attribute.Int64("web_sessions_deleted", webSessionsDeleted),
		attribute.Bool("backlog_remains", backlogRemains),
	)
	w.logger.Info("retention cycle complete",
		"spans_aggregated", totalAggregated,
		"errors_sampled", totalSampled,
		"spans_deleted", spansDeleted,
		"boring_deleted", boringDeleted,
		"error_samples_deleted", errorSamplesDeleted,
		"aggregates_deleted", aggregatesDeleted,
		"metrics_deleted", metricsDeleted,
		"metric_rollups_deleted", metricRollupsDeleted,
		"logs_deleted", logsDeleted,
		"e2e_users_deleted", e2eUsersDeleted,
		"web_sessions_deleted", webSessionsDeleted,
		"backlog_remains", backlogRemains,
	)
	return nil
}
