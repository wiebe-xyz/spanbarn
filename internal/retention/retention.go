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
	DeleteSpansByIDs(ids []int64) (int64, error)
	DeleteSpansByMaxID(maxID int64) (int64, error)
	DeleteSpansOlderThan(cutoff time.Time) (int64, error)
	DeleteBoringSpans(olderThan, newerThan time.Time, slowThresholdUS int64) (int64, error)
	InsertErrorSamples(spans []repository.Span) error
	DeleteErrorSamplesOlderThan(cutoff time.Time) (int64, error)
	DeleteAggregatesOlderThan(cutoff time.Time) (int64, error)
	GetSetting(key string) (string, error)
}

// Config controls the retention worker's behaviour.
type Config struct {
	FullRetentionHours        int           // hours to keep ALL spans (default 4)
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
				w.logger.Warn("retention cycle failed, will retry next tick", "error", lastErr)
			}
		}
	}
}

// RunOnce executes a single retention cycle:
//  1. Fetch spans older than FullRetentionHours in batches
//  2. Separate error/slow spans and insert into error_samples
//  3. Aggregate all spans and persist aggregates
//  4. Delete old spans, error_samples, and aggregates
// effectiveConfig returns the retention config, overriding with DB settings where present.
func (w *RetentionWorker) effectiveConfig() Config {
	cfg := w.cfg
	if v, err := w.repo.GetSetting("retention_full_hours"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.FullRetentionHours = n
		}
	}
	if v, err := w.repo.GetSetting("retention_interesting_hours"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.InterestingRetentionHours = n
		}
	}
	if v, err := w.repo.GetSetting("retention_aggregated_days"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.AggregateRetentionDays = n
		}
	}
	if v, err := w.repo.GetSetting("retention_error_days"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ErrorRetentionDays = n
		}
	}
	return cfg
}

func (w *RetentionWorker) RunOnce(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "retention.cycle")
	defer span.End()

	cfg := w.effectiveConfig()

	now := time.Now().UTC()
	fullCutoff := now.Add(-time.Duration(cfg.FullRetentionHours) * time.Hour)
	interestingCutoff := now.Add(-time.Duration(cfg.InterestingRetentionHours) * time.Hour)
	errorCutoff := now.Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour)
	aggCutoff := now.Add(-time.Duration(cfg.AggregateRetentionDays) * 24 * time.Hour)

	var totalAggregated int64
	var totalSampled int64
	var spansDeleted int64
	var boringDropped int64

	// Phase 1: Drop boring (non-error, non-slow) spans that are past full
	// retention but still within interesting retention. These don't need
	// aggregation yet — the interesting ones survive until phase 2.
	if interestingCutoff.Before(fullCutoff) {
		dropped, err := w.repo.DeleteBoringSpans(fullCutoff, interestingCutoff, cfg.SlowThresholdUS)
		if err != nil {
			return err
		}
		boringDropped = dropped
	}

	// Phase 2: Process spans older than interesting retention — aggregate all
	// remaining spans (the interesting ones that survived phase 1) and delete.
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

	span.SetAttributes(
		attribute.Int64("spans_aggregated", totalAggregated),
		attribute.Int64("errors_sampled", totalSampled),
		attribute.Int64("spans_deleted", spansDeleted),
		attribute.Int64("boring_dropped", boringDropped),
		attribute.Int64("error_samples_deleted", errorSamplesDeleted),
		attribute.Int64("aggregates_deleted", aggregatesDeleted),
	)

	w.logger.Info("retention cycle complete",
		"spans_aggregated", totalAggregated,
		"errors_sampled", totalSampled,
		"spans_deleted", spansDeleted,
		"boring_dropped", boringDropped,
		"error_samples_deleted", errorSamplesDeleted,
		"aggregates_deleted", aggregatesDeleted,
	)

	return nil
}
