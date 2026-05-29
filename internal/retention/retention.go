package retention

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var tracer = otel.Tracer("spanbarn/retention")

const defaultBatchSize = 5000

// Repository defines the data-access methods the retention worker needs.
type Repository interface {
	GetSpansForAggregation(cutoff time.Time, limit int) ([]repository.Span, error)
	GetBoringTraceSpans(cutoff time.Time, slowThresholdUS int64, limit int) ([]repository.Span, error)
	DeleteSpansByIDs(ids []int64) (int64, error)
	DeleteSpansByMaxID(maxID int64) (int64, error)
	DeleteSpansOlderThan(cutoff time.Time) (int64, error)
	InsertErrorSamples(spans []repository.Span) error
	DeleteErrorSamplesOlderThan(cutoff time.Time) (int64, error)
	DeleteAggregatesOlderThan(cutoff time.Time) (int64, error)
	DeleteExpiredE2EUsers(now time.Time) (int64, error)
	GetSetting(key string) (string, error)
}

// Config controls the retention worker's behaviour.
type Config struct {
	FullRetentionHours        int           // hours to keep ALL spans (default 4)
	BoringTraceHours          int           // hours to keep boring (non-error, non-slow) traces (default 4)
	InterestingRetentionHours int           // hours to keep error/slow spans after full retention expires (default 168 = 7d)
	ErrorRetentionDays        int           // days to keep error samples (default 30)
	AggregateRetentionDays    int           // days to keep aggregates (default 365)
	SlowThresholdUS           int64         // microseconds above which a span is "slow"
	Interval                  time.Duration // how often to run (default 5m)
}

func (c Config) withDefaults() Config {
	if c.FullRetentionHours <= 0 {
		c.FullRetentionHours = 4
	}
	if c.BoringTraceHours <= 0 {
		c.BoringTraceHours = 4
	}
	if c.InterestingRetentionHours <= 0 {
		c.InterestingRetentionHours = 168
	}
	if c.ErrorRetentionDays <= 0 {
		c.ErrorRetentionDays = 30
	}
	if c.AggregateRetentionDays <= 0 {
		c.AggregateRetentionDays = 365
	}
	if c.SlowThresholdUS <= 0 {
		c.SlowThresholdUS = 1_000_000 // 1 second
	}
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	return c
}

// RetentionWorker manages span lifecycle: aggregate old spans, sample
// errors/slow spans, and delete data past its retention window.
type RetentionWorker struct {
	repo       Repository
	aggregator *aggregation.Aggregator
	cfg        Config
	logger     *slog.Logger
}

// NewRetentionWorker creates a new retention worker.
func NewRetentionWorker(repo Repository, aggregator *aggregation.Aggregator, cfg Config, logger *slog.Logger) *RetentionWorker {
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
	if n, ok := readInt("boring_trace_hours"); ok {
		cfg.BoringTraceHours = n
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
	return cfg
}

// RunOnce executes a single retention cycle:
//  1. Aggregate-and-delete boring traces (non-error, non-slow) older than boring_trace_hours
//  2. Fetch spans older than full_retention_hours in batches, sample errors, aggregate, delete
//  3. Delete old error_samples and aggregates
func (w *RetentionWorker) RunOnce(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "retention.cycle")
	defer span.End()

	cfg := w.effectiveConfig()

	now := time.Now().UTC()
	boringCutoff := now.Add(-time.Duration(cfg.BoringTraceHours) * time.Hour)
	interestingCutoff := now.Add(-time.Duration(cfg.InterestingRetentionHours) * time.Hour)
	errorCutoff := now.Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour)
	aggCutoff := now.Add(-time.Duration(cfg.AggregateRetentionDays) * 24 * time.Hour)

	// Phase 1: aggregate-then-delete boring traces on the shorter TTL.
	boringDeleted, err := w.processBoring(ctx, boringCutoff, cfg.SlowThresholdUS)
	if err != nil {
		return err
	}

	// Phase 2: aggregate-then-delete all remaining old spans (error/slow included).
	var totalAggregated, totalSampled, spansDeleted int64
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

		var samples []repository.Span
		for _, s := range batch {
			if s.Status == "error" || s.DurationUs > cfg.SlowThresholdUS {
				samples = append(samples, s)
			}
		}
		if len(samples) > 0 {
			if err := w.repo.InsertErrorSamples(samples); err != nil {
				return err
			}
			totalSampled += int64(len(samples))
		}

		aggs, err := w.aggregator.AggregateSpans(ctx, batch)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
		if err := w.aggregator.Persist(ctx, aggs); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
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
			return err
		}
		spansDeleted += deleted

		if len(batch) < defaultBatchSize {
			break
		}
	}

	errorSamplesDeleted, err := w.repo.DeleteErrorSamplesOlderThan(errorCutoff)
	if err != nil {
		return err
	}
	aggregatesDeleted, err := w.repo.DeleteAggregatesOlderThan(aggCutoff)
	if err != nil {
		return err
	}
	e2eUsersDeleted, err := w.repo.DeleteExpiredE2EUsers(now)
	if err != nil {
		return err
	}

	span.SetAttributes(
		attribute.Int64("boring_spans_deleted", boringDeleted),
		attribute.Int64("spans_aggregated", totalAggregated),
		attribute.Int64("errors_sampled", totalSampled),
		attribute.Int64("spans_deleted", spansDeleted),
		attribute.Int64("error_samples_deleted", errorSamplesDeleted),
		attribute.Int64("aggregates_deleted", aggregatesDeleted),
		attribute.Int64("e2e_users_deleted", e2eUsersDeleted),
	)
	w.logger.Info("retention cycle complete",
		"boring_spans_deleted", boringDeleted,
		"spans_aggregated", totalAggregated,
		"errors_sampled", totalSampled,
		"spans_deleted", spansDeleted,
		"error_samples_deleted", errorSamplesDeleted,
		"aggregates_deleted", aggregatesDeleted,
		"e2e_users_deleted", e2eUsersDeleted,
	)
	return nil
}

// processBoring aggregates then deletes boring traces older than boringCutoff.
// Returns the number of spans deleted.
func (w *RetentionWorker) processBoring(ctx context.Context, boringCutoff time.Time, slowThresholdUS int64) (int64, error) {
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}

		batch, err := w.repo.GetBoringTraceSpans(boringCutoff, slowThresholdUS, defaultBatchSize)
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}

		aggs, err := w.aggregator.AggregateSpans(ctx, batch)
		if err != nil {
			return total, err
		}
		if len(aggs) > 0 {
			if err := w.aggregator.Persist(ctx, aggs); err != nil {
				return total, err
			}
		}

		// Delete in chunks to stay under SQLite's parameter limit.
		ids := make([]int64, len(batch))
		for i, s := range batch {
			ids[i] = s.ID
		}
		for len(ids) > 0 {
			chunk := ids
			if len(chunk) > 500 {
				chunk = ids[:500]
			}
			ids = ids[len(chunk):]
			n, err := w.repo.DeleteSpansByIDs(chunk)
			if err != nil {
				return total, err
			}
			total += n
		}

		if len(batch) < defaultBatchSize {
			break
		}
	}
	return total, nil
}
