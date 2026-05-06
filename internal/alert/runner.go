package alert

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// ProjectLister provides a list of all projects for the runner to evaluate.
type ProjectLister interface {
	ListProjectIDs() ([]int64, error)
}

// Runner periodically evaluates alerts for all projects.
type Runner struct {
	evaluator *Evaluator
	projects  ProjectLister
	interval  time.Duration
	logger    *slog.Logger
}

// NewRunner creates a Runner that evaluates alerts at the given interval.
func NewRunner(evaluator *Evaluator, projects ProjectLister, interval time.Duration, logger *slog.Logger) *Runner {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		evaluator: evaluator,
		projects:  projects,
		interval:  interval,
		logger:    logger,
	}
}

// Run starts the evaluation loop, blocking until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info("alert runner started", "interval", r.interval)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("alert runner stopped")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	ctx, span := alertTracer.Start(ctx, "alert.runner_tick")
	defer span.End()

	ids, err := r.projects.ListProjectIDs()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		r.logger.Error("list projects for alert evaluation", "error", err)
		return
	}

	span.SetAttributes(attribute.Int("project_count", len(ids)))

	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := r.evaluator.Evaluate(ctx, id); err != nil {
			r.logger.Error("evaluate alerts", "projectID", id, "error", err)
		}
	}
}
