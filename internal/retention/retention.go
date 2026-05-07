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
	DeleteSpansOlderThan(cutoff time.Time) (int64, error)
	InsertErrorSamples(spans []repository.Span) error
	DeleteErrorSamplesOlderThan(cutoff time.Time) (int64, error)
	DeleteAggregatesOlderThan(cutoff time.Time) (int64, error)
	GetSetting(key string) (string, error)
}

// Config controls the retention worker's behaviour.
type Config struct {
	FullRetentionHours     int           // hours to keep full spans (default 4)
	ErrorRetentionDays     int           // days to keep error samples (default 30)
	AggregateRetentionDays int           // days to keep aggregates (default 365)
	SlowThresholdUS        int64         // microseconds above which a span is "slow"
	Interval               time.Duration // how often to run (default 5m)
}

func (c Config) withDefaults() Config {
	if c.FullRetentionHours <= 0 {
		c.FullRetentionHours = 4
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
			if err := w.RunOnce(ctx); err != nil {
				w.logger.Error("retention cycle failed", "error", err)
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
	spanCutoff := now.Add(-time.Duration(cfg.FullRetentionHours) * time.Hour)
	errorCutoff := now.Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour)
	aggCutoff := now.Add(-time.Duration(cfg.AggregateRetentionDays) * 24 * time.Hour)

	var totalAggregated int64
	var totalSampled int64
	var spansDeleted int64

	// Process old spans in batches.
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batch, err := w.repo.GetSpansForAggregation(spanCutoff, defaultBatchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}

		// Separate error and slow spans for sampling.
		var samples []repository.Span
		for _, s := range batch {
			if s.Status == "error" || s.DurationUs > w.cfg.SlowThresholdUS {
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

		// Delete the processed spans by ID so the next batch fetch
		// doesn't re-read the same rows.
		ids := make([]int64, len(batch))
		for i, s := range batch {
			ids[i] = s.ID
		}
		deleted, err := w.repo.DeleteSpansByIDs(ids)
		if err != nil {
			return err
		}
		spansDeleted += deleted

		// If we got fewer than the batch size, we've consumed everything.
		if len(batch) < defaultBatchSize {
			break
		}
	}

	// Delete old error samples.
	errorSamplesDeleted, err := w.repo.DeleteErrorSamplesOlderThan(errorCutoff)
	if err != nil {
		return err
	}

	// Delete old aggregates.
	aggregatesDeleted, err := w.repo.DeleteAggregatesOlderThan(aggCutoff)
	if err != nil {
		return err
	}

	span.SetAttributes(
		attribute.Int64("spans_aggregated", totalAggregated),
		attribute.Int64("errors_sampled", totalSampled),
		attribute.Int64("spans_deleted", spansDeleted),
		attribute.Int64("error_samples_deleted", errorSamplesDeleted),
		attribute.Int64("aggregates_deleted", aggregatesDeleted),
	)

	w.logger.Info("retention cycle complete",
		"spans_aggregated", totalAggregated,
		"errors_sampled", totalSampled,
		"spans_deleted", spansDeleted,
		"error_samples_deleted", errorSamplesDeleted,
		"aggregates_deleted", aggregatesDeleted,
	)

	return nil
}
