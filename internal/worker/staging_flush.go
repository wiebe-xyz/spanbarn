package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
)

// StagingRepository is the data access the flusher needs.
type StagingRepository interface {
	ReadyStagingTraceIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error)
	GetStagingSpansByTraceIDs(ctx context.Context, traceIDs []string) ([]repository.Span, error)
	CommitStagingFlush(ctx context.Context, traceIDs []string, interesting []repository.Span) error
	DeleteStagingOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	CountStagingRows(ctx context.Context) (int64, error)
	InsertPromptRecords(records []repository.PromptRecord) error
}

// StagingFlusherConfig tunes the flush and GC loops.
type StagingFlusherConfig struct {
	Window          time.Duration // a trace is flushed once its oldest staged span is this old
	FlushInterval   time.Duration // how often to look for ready traces
	GCInterval      time.Duration // how often the hard-age backstop runs
	MaxAge          time.Duration // staging rows older than this are dropped unconditionally
	BatchTraces     int           // max ready traces processed per flush transaction
	SlowThresholdUs int64         // classification: spans slower than this make a trace interesting
}

// StagingFlusher moves complete traces out of spans_staging: it feeds every span
// to the accumulator, classifies whole traces, stores only the interesting ones
// in the indexed spans table, and deletes the processed rows — with a hard-age GC
// backstop so staging can never grow without bound.
type StagingFlusher struct {
	repo         StagingRepository
	accumulator  SpanAccumulator
	boringPolicy BoringPolicyReader
	floor        *sampling.MinuteFloor
	cfg          StagingFlusherConfig
	logger       *slog.Logger
}

// NewStagingFlusher creates a flusher, applying sane defaults for any unset config.
func NewStagingFlusher(repo StagingRepository, cfg StagingFlusherConfig, logger *slog.Logger) *StagingFlusher {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Window <= 0 {
		cfg.Window = 90 * time.Second
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 10 * time.Second
	}
	if cfg.GCInterval <= 0 {
		cfg.GCInterval = time.Minute
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 15 * time.Minute
	}
	if cfg.BatchTraces <= 0 {
		cfg.BatchTraces = 200
	}
	return &StagingFlusher{repo: repo, cfg: cfg, logger: logger}
}

// maxFlushIterationsPerTick caps how many BatchTraces batches a single flush tick
// processes before yielding, so the flush loop always returns to let the GC and
// the span-staging inserts get the single connection.
const maxFlushIterationsPerTick = 8

func (f *StagingFlusher) SetAccumulator(a SpanAccumulator)        { f.accumulator = a }
func (f *StagingFlusher) SetBoringPolicy(p BoringPolicyReader)    { f.boringPolicy = p }
func (f *StagingFlusher) SetMinuteFloor(fl *sampling.MinuteFloor) { f.floor = fl }

// Run flushes ready traces and GCs the staging table until ctx is cancelled. The
// GC runs in its own goroutine so a busy flush loop can never starve it — that is
// what guarantees spans_staging stays bounded even under sustained overload.
func (f *StagingFlusher) Run(ctx context.Context) {
	go f.gcLoop(ctx)
	f.flushLoop(ctx)
}

func (f *StagingFlusher) flushLoop(ctx context.Context) {
	t := time.NewTicker(f.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Process ready traces, but cap iterations per tick so the flush
			// yields the single connection back to staging inserts and returns
			// to the ticker regularly instead of monopolizing the loop.
			for i := 0; i < maxFlushIterationsPerTick; i++ {
				n, err := f.flushOnce(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					f.logger.Error("staging flush error", "error", err)
					break
				}
				if n < f.cfg.BatchTraces {
					break // caught up
				}
			}
		}
	}
}

func (f *StagingFlusher) gcLoop(ctx context.Context) {
	t := time.NewTicker(f.cfg.GCInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.gcOnce(ctx)
		}
	}
}

// flushOnce processes up to BatchTraces ready traces and returns how many it
// handled (0 means nothing was ready).
func (f *StagingFlusher) flushOnce(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-f.cfg.Window)
	traceIDs, err := f.repo.ReadyStagingTraceIDs(ctx, cutoff, f.cfg.BatchTraces)
	if err != nil {
		return 0, err
	}
	if len(traceIDs) == 0 {
		return 0, nil
	}

	spans, err := f.repo.GetStagingSpansByTraceIDs(ctx, traceIDs)
	if err != nil {
		return 0, err
	}

	// Aggregate every span (boring included) before classification, exactly like
	// the inline path — now over complete traces.
	if f.accumulator != nil {
		for i := range spans {
			f.accumulator.Add(spans[i])
		}
	}
	interesting := classifySpansForStorage(spans, f.cfg.SlowThresholdUs, f.boringPolicy, f.floor)

	// Atomically move interesting spans to the indexed table and delete all
	// processed rows for these traces.
	if err := f.repo.CommitStagingFlush(ctx, traceIDs, interesting); err != nil {
		return 0, err
	}
	if promptRecs := extractPromptRecords(interesting); len(promptRecs) > 0 {
		if err := f.repo.InsertPromptRecords(promptRecs); err != nil {
			f.logger.Warn("staging flush: insert prompt records", "count", len(promptRecs), "error", err)
		}
	}
	f.logger.Debug("staging flush", "traces", len(traceIDs), "spans", len(spans), "stored", len(interesting))
	return len(traceIDs), nil
}

// gcOnce is the bounded-growth backstop: drop anything older than MaxAge even if
// the flush never processed it, and surface staging depth for observability.
func (f *StagingFlusher) gcOnce(ctx context.Context) {
	cutoff := time.Now().Add(-f.cfg.MaxAge)
	deleted, err := f.repo.DeleteStagingOlderThan(ctx, cutoff)
	if err != nil {
		if ctx.Err() == nil {
			f.logger.Error("staging gc error", "error", err)
		}
		return
	}
	if deleted > 0 {
		// Non-zero means the flush fell behind and we shed unprocessed spans.
		f.logger.Warn("staging gc dropped rows older than max age", "deleted", deleted, "max_age", f.cfg.MaxAge.String())
	}
	if n, err := f.repo.CountStagingRows(ctx); err == nil {
		f.logger.Info("staging depth", "rows", n)
	}
}
