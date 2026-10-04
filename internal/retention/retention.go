package retention

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

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
	// maxPromptRowsPerCycle caps the prompt records one cycle deletes. The rows
	// are several KB each, and the first cycle after this window was introduced
	// faces weeks of backlog; draining that in one call would hold the cycle open
	// instead of letting it re-measure the disk and run the other deletes.
	maxPromptRowsPerCycle = 20_000
)

// Repository defines the data-access methods the retention worker needs.
type Repository interface {
	CountSpansOlderThan(cutoff time.Time) (int64, error)
	GetSpansForAggregation(cutoff time.Time, limit int) ([]repository.Span, error)
	DeleteSpansByMaxIDRefreshing(ctx context.Context, maxID int64) (int64, error)
	BackfillTraceStructure(ctx context.Context, max int64) (int64, bool, error)
	DeleteSpansOlderThan(cutoff time.Time) (int64, error)
	DeleteExpiredBoringSpans(ctx context.Context, now time.Time) (int64, error)
	DeleteExpiredTraceSummaries(ctx context.Context, now time.Time) (int64, error)
	DeleteTraceSummariesOlderThan(ctx context.Context, interestingCutoff, errorCutoff time.Time) (int64, error)
	InsertErrorSamples(spans []repository.Span) error
	DeleteErrorSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteAggregatesOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteExpiredE2EUsers(now time.Time) (int64, error)
	DeleteExpiredWebSessions(now time.Time) (int64, error)
	DeleteMetricsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteMetricRollupsOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	DeleteMetricRollupsOlderThanLimited(ctx context.Context, cutoff time.Time, max int64) (int64, bool, error)
	DeleteCoarseRollupsOlderThanLimited(ctx context.Context, step int64, cutoff time.Time, max int64) (int64, bool, error)
	MetricRollupWatermark(ctx context.Context, step int64) (time.Time, bool, error)
	DeleteLogsOlderThan(ctx context.Context, cutoff, errorLogCutoff time.Time) (int64, error)
	DeletePromptRecordsOlderThanLimited(ctx context.Context, cutoff time.Time, max int64) (int64, bool, error)
	GetSetting(key string) (string, error)
	ListProjectIDs() ([]int64, error)
	EvictProjectTracesOlderThan(ctx context.Context, projectID int64, cutoff time.Time) (int64, error)
	ProjectNonErrorTraceCountCutoff(ctx context.Context, projectID int64, keepN int) (time.Time, bool, error)
}

// RetentionWorker manages span lifecycle: aggregate old spans, copy error,
// slow and durable spans to error_samples, and delete data past its retention
// window.
type RetentionWorker struct {
	repo       Repository
	aggregator Aggregator
	cfg        Config
	logger     *slog.Logger

	// warnObsoleteFullHours keeps the retention_full_hours deprecation notice to
	// one line per process instead of one per cycle.
	warnObsoleteFullHours sync.Once
	// warnNoAutoVacuum likewise keeps the auto_vacuum=NONE warning to one line.
	warnNoAutoVacuum sync.Once

	// ballast is the reserved space surrendered during emergency eviction.
	ballast     *repository.Ballast
	ballastOnce sync.Once

	// pressured is set when the last cycle found the volume critical, so Run
	// tightens its interval instead of waiting a full period to look again.
	pressured atomic.Bool

	// stats backs Stats(), which the storage self-metrics read.
	stats statsState
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

// pressuredInterval is how often retention re-checks while the volume is
// critical. A five-minute wait is fine when there is room; when the disk is
// filling it is most of the margin.
const pressuredInterval = 30 * time.Second

// nextInterval returns how long to wait before the next cycle: the configured
// period normally, a much shorter one while the volume is under pressure.
func (w *RetentionWorker) nextInterval() time.Duration {
	if w.pressured.Load() && w.cfg.Interval > pressuredInterval {
		return pressuredInterval
	}
	return w.cfg.Interval
}

// Run starts the retention loop, ticking at cfg.Interval until ctx is cancelled.
// Under disk pressure it ticks considerably faster — see nextInterval.
func (w *RetentionWorker) Run(ctx context.Context) {
	timer := time.NewTimer(w.cfg.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("retention worker stopped")
			return
		case <-timer.C:
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
			timer.Reset(w.nextInterval())
		}
	}
}

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

// cycleCutoffs holds the age cutoffs of one retention cycle.
type cycleCutoffs struct {
	now         time.Time
	interesting time.Time
	errors      time.Time
	aggregates  time.Time
	metrics     time.Time
	logs        time.Time
	errorLogs   time.Time
	prompts     time.Time
}

func newCycleCutoffs(now time.Time, cfg Config) cycleCutoffs {
	return cycleCutoffs{
		now:         now,
		interesting: now.Add(-time.Duration(cfg.InterestingRetentionHours) * time.Hour),
		errors:      now.Add(-time.Duration(cfg.ErrorRetentionDays) * 24 * time.Hour),
		aggregates:  now.Add(-time.Duration(cfg.AggregateRetentionDays) * 24 * time.Hour),
		metrics:     now.Add(-time.Duration(cfg.MetricsRetentionDays) * 24 * time.Hour),
		logs:        now.Add(-time.Duration(cfg.LogRetentionHours) * time.Hour),
		errorLogs:   now.Add(-time.Duration(cfg.ErrorLogRetentionDays) * 24 * time.Hour),
		prompts:     now.Add(-time.Duration(cfg.PromptRetentionDays) * 24 * time.Hour),
	}
}

// cycleStats counts what one retention cycle did.
type cycleStats struct {
	spansAggregated      int64
	errorsSampled        int64
	spansDeleted         int64
	boringDeleted        int64
	errorSamplesDeleted  int64
	aggregatesDeleted    int64
	metricsDeleted       int64
	rollupRowsDeleted    int64
	rollupBacklogRemains bool
	logsDeleted          int64
	promptsDeleted       int64
	promptBacklogRemains bool
	e2eUsersDeleted      int64
	webSessionsDeleted   int64
	projectTracesEvicted int64
	backlogRemains       bool
}

func (c *cycleStats) attributes() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64("spans_aggregated", c.spansAggregated),
		attribute.Int64("errors_sampled", c.errorsSampled),
		attribute.Int64("spans_deleted", c.spansDeleted),
		attribute.Int64("boring_deleted", c.boringDeleted),
		attribute.Int64("error_samples_deleted", c.errorSamplesDeleted),
		attribute.Int64("aggregates_deleted", c.aggregatesDeleted),
		attribute.Int64("metrics_deleted", c.metricsDeleted),
		attribute.Int64("rollup_rows_deleted", c.rollupRowsDeleted),
		attribute.Bool("rollup_backlog_remains", c.rollupBacklogRemains),
		attribute.Int64("logs_deleted", c.logsDeleted),
		attribute.Int64("prompts_deleted", c.promptsDeleted),
		attribute.Bool("prompt_backlog_remains", c.promptBacklogRemains),
		attribute.Int64("e2e_users_deleted", c.e2eUsersDeleted),
		attribute.Int64("web_sessions_deleted", c.webSessionsDeleted),
		attribute.Int64("project_traces_evicted", c.projectTracesEvicted),
		attribute.Bool("backlog_remains", c.backlogRemains),
	}
}

func (c *cycleStats) logArgs() []any {
	return []any{
		"spans_aggregated", c.spansAggregated,
		"errors_sampled", c.errorsSampled,
		"spans_deleted", c.spansDeleted,
		"boring_deleted", c.boringDeleted,
		"error_samples_deleted", c.errorSamplesDeleted,
		"aggregates_deleted", c.aggregatesDeleted,
		"metrics_deleted", c.metricsDeleted,
		"rollup_rows_deleted", c.rollupRowsDeleted,
		"rollup_backlog_remains", c.rollupBacklogRemains,
		"logs_deleted", c.logsDeleted,
		"prompts_deleted", c.promptsDeleted,
		"prompt_backlog_remains", c.promptBacklogRemains,
		"e2e_users_deleted", c.e2eUsersDeleted,
		"web_sessions_deleted", c.webSessionsDeleted,
		"project_traces_evicted", c.projectTracesEvicted,
		"backlog_remains", c.backlogRemains,
	}
}

// RunOnce executes a single retention cycle:
//  1. Fetch spans older than full_retention_hours in batches, sample errors, aggregate, delete
//  2. Delete old error_samples and aggregates
//
// Each cycle is capped at maxSpansPerCycle deletions to keep write-lock hold
// times short. If the cap is reached, backlog_remains is logged so operators
// know the next tick will continue draining.
func (w *RetentionWorker) RunOnce(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "retention.cycle")
	defer span.End()

	cfg := w.applyDiskPressure(ctx, w.effectiveConfig())
	cut := newCycleCutoffs(time.Now().UTC(), cfg)
	var st cycleStats

	w.warnOnLargeBacklog(cut.interesting)
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := w.drainOldSpans(ctx, span, cfg, cut.interesting, &st); err != nil {
		return err
	}
	if err := w.purgeSpanDerived(ctx, cfg, cut, &st); err != nil {
		return err
	}
	if err := w.purgeHousekeeping(ctx, cut, &st); err != nil {
		return err
	}

	span.SetAttributes(st.attributes()...)
	w.logger.Info("retention cycle complete", st.logArgs()...)
	w.recordCycle(&st)
	return nil
}

// warnOnLargeBacklog counts spans pending deletion and warns if the backlog is
// unexpectedly large.
func (w *RetentionWorker) warnOnLargeBacklog(cutoff time.Time) {
	pending, err := w.repo.CountSpansOlderThan(cutoff)
	if err != nil {
		w.logger.Warn("retention: count pending spans failed", "error", err)
		return
	}
	w.logger.Info("retention: pending spans", "count", pending)
	if pending > largeBacklogWarn {
		w.logger.Warn("retention: large backlog detected, drain will span multiple cycles",
			"pending_spans", pending, "per_cycle_cap", maxSpansPerCycle)
	}
}

// drainOldSpans aggregate-then-deletes all old spans (error/slow included),
// capped at maxSpansPerCycle total to bound write-lock hold time. The write
// mutex is held only for the duration of one batch, then released for
// cfg.BatchYield so the span-insert worker can drain the Redis queue.
func (w *RetentionWorker) drainOldSpans(ctx context.Context, span trace.Span, cfg Config, cutoff time.Time, st *cycleStats) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batch, err := w.repo.GetSpansForAggregation(cutoff, defaultBatchSize)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}

		var batchErr error
		w.lockBatch(ctx, cfg.BatchYield, func() {
			batchErr = w.aggregateAndDelete(ctx, cfg, batch, st)
		})
		if batchErr != nil {
			span.RecordError(batchErr)
			span.SetStatus(codes.Error, batchErr.Error())
			return batchErr
		}
		if len(batch) < defaultBatchSize {
			return nil
		}
		if st.spansDeleted >= maxSpansPerCycle {
			st.backlogRemains = true
			return nil
		}
	}
}

// aggregateAndDelete copies the error, slow and durable spans of one batch to
// error_samples, aggregates the batch and deletes it. A durable span is one
// the ingest floor kept as an operation's clean example; copying it keeps that
// trace readable until the error cutoff, matching its trace summary. A span
// that is both error/slow and durable is copied once.
func (w *RetentionWorker) aggregateAndDelete(ctx context.Context, cfg Config, batch []repository.Span, st *cycleStats) error {
	var samples []repository.Span
	for _, s := range batch {
		if s.Status == "error" || s.DurationUs > cfg.SlowThresholdUS || s.Durable {
			samples = append(samples, s)
		}
	}
	if len(samples) > 0 {
		if err := w.repo.InsertErrorSamples(samples); err != nil {
			return err
		}
		st.errorsSampled += int64(len(samples))
	}

	aggs, err := w.aggregator.AggregateSpans(ctx, batch)
	if err != nil {
		return err
	}
	if err := w.aggregator.Persist(ctx, aggs); err != nil {
		return err
	}
	st.spansAggregated += int64(len(batch))

	maxID := batch[0].ID
	for _, s := range batch[1:] {
		if s.ID > maxID {
			maxID = s.ID
		}
	}
	deleted, err := w.repo.DeleteSpansByMaxIDRefreshing(ctx, maxID)
	if err != nil {
		return err
	}
	st.spansDeleted += deleted
	w.logger.Info("retention: batch deleted", "deleted", deleted, "cycle_total", st.spansDeleted)
	return nil
}

// purgeSpanDerived deletes the data derived from spans once it ages out:
// boring spans, trace summaries, error samples, aggregates, metrics and rollups.
func (w *RetentionWorker) purgeSpanDerived(ctx context.Context, cfg Config, cut cycleCutoffs, st *cycleStats) error {
	var err error

	// Fast boring-span cleanup: delete sampled-boring spans whose stamped
	// expires_at has passed. Classification writes expires_at (= ingested_at +
	// boring retention) at storage time, so this is a bounded seek of the partial
	// idx_spans_expires index — it no longer scans the table classifying by
	// status/duration_us (which had wedged the writer for 30s+).
	if st.boringDeleted, err = w.repo.DeleteExpiredBoringSpans(ctx, cut.now); err != nil {
		return err
	}

	// Clean up trace_summaries in lockstep with the spans they describe: early
	// for boring-sampled traces (stamped expires_at), then non-error at the
	// interesting cutoff, and error or durable traces at the error cutoff
	// (matching error_samples, which holds their spans), so the trace list drops
	// rows exactly when its spans go.
	if _, err := w.repo.DeleteExpiredTraceSummaries(ctx, cut.now); err != nil {
		return err
	}
	if _, err := w.repo.DeleteTraceSummariesOlderThan(ctx, cut.interesting, cut.errors); err != nil {
		return err
	}

	w.backfillTraceStructure(ctx)

	if st.errorSamplesDeleted, err = w.repo.DeleteErrorSamplesOlderThan(ctx, cut.errors); err != nil {
		return err
	}
	if st.aggregatesDeleted, err = w.repo.DeleteAggregatesOlderThan(ctx, cut.aggregates); err != nil {
		return err
	}
	if st.metricsDeleted, err = w.repo.DeleteMetricsOlderThan(ctx, cut.metrics); err != nil {
		return err
	}
	st.rollupRowsDeleted, st.rollupBacklogRemains, err = w.deleteRollupTiers(ctx, cfg, cut.now, maxRollupRowsPerCycle)
	return err
}

// purgeHousekeeping deletes aged logs, prompt records, E2E users and web
// sessions, then enforces the per-project retention caps.
func (w *RetentionWorker) purgeHousekeeping(ctx context.Context, cut cycleCutoffs, st *cycleStats) error {
	var err error
	if st.logsDeleted, err = w.repo.DeleteLogsOlderThan(ctx, cut.logs, cut.errorLogs); err != nil {
		return err
	}
	if st.promptsDeleted, st.promptBacklogRemains, err = w.repo.DeletePromptRecordsOlderThanLimited(ctx, cut.prompts, maxPromptRowsPerCycle); err != nil {
		return err
	}
	if st.e2eUsersDeleted, err = w.repo.DeleteExpiredE2EUsers(cut.now); err != nil {
		return err
	}
	// Web sessions past their absolute cap are already unusable (the session
	// middleware enforces absolute_expires_at); this prunes the rows.
	if st.webSessionsDeleted, err = w.repo.DeleteExpiredWebSessions(cut.now); err != nil {
		return err
	}

	// Enforce per-project retention caps (max age in hours and/or max non-error
	// trace count). Only shortens retention; never touches errors, pinned traces,
	// or metrics.
	st.projectTracesEvicted, err = w.evictPerProjectCaps(ctx, cut.now)
	return err
}

// Per-project retention caps live in project_caps.go; disk-pressure tiering
// lives in pressure.go.
