package retention

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// runPhases runs the cycle's three phases, each under its own span. The
// database calls inside a phase are counted onto that span as db.calls
// attributes; otelsql creates no span per statement under it.
func (w *RetentionWorker) runPhases(ctx context.Context, cycle trace.Span, cfg Config, cut cycleCutoffs, st *cycleStats) error {
	if err := runPhase(ctx, "retention.drain_spans", func(ctx context.Context) error {
		return w.drainOldSpans(ctx, cycle, cfg, cut.interesting, st)
	}); err != nil {
		return err
	}
	if err := runPhase(ctx, "retention.purge_span_derived", func(ctx context.Context) error {
		return w.purgeSpanDerived(ctx, cfg, cut, st)
	}); err != nil {
		return err
	}
	return runPhase(ctx, "retention.purge_housekeeping", func(ctx context.Context) error {
		return w.purgeHousekeeping(ctx, cut, st)
	})
}

func runPhase(ctx context.Context, name string, run func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	ctx, calls := repository.WithStatementCounts(ctx)
	err := run(ctx)
	span.SetAttributes(calls.Attributes()...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
