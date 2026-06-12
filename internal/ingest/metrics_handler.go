package ingest

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	metricsChannelSize  = 256
	metricsFlushSize    = 500
	metricsFlushInterval = 100 * time.Millisecond
)

// MetricsRepository is the write-side subset of repository.Repository needed
// by MetricsHandler. Using an interface keeps the handler testable without a DB.
type MetricsRepository interface {
	InsertMetrics(ctx context.Context, recs []model.MetricRecord) error
}

// MetricsHandler buffers incoming OTLP metric data points and writes them to
// SQLite in batches. It does not use the spool — losing a flush on crash is
// acceptable for metrics.
type MetricsHandler struct {
	ch     chan []model.MetricRecord
	repo   MetricsRepository
	logger *slog.Logger
}

// NewMetricsHandler creates a MetricsHandler ready to call Run.
func NewMetricsHandler(repo MetricsRepository, logger *slog.Logger) *MetricsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &MetricsHandler{
		ch:     make(chan []model.MetricRecord, metricsChannelSize),
		repo:   repo,
		logger: logger,
	}
}

// Enqueue adds a batch of metric records to the in-memory channel.
// If the channel is full the batch is dropped and a warning is logged.
func (h *MetricsHandler) Enqueue(recs []model.MetricRecord) {
	if len(recs) == 0 {
		return
	}
	select {
	case h.ch <- recs:
	default:
		h.logger.Warn("metrics channel full, dropping batch", "count", len(recs))
	}
}

// Run drains the channel and writes batches to the repository until ctx is
// cancelled. After cancellation it flushes remaining buffered records before
// returning. Blocks until done — intended for use with safeGo.
func (h *MetricsHandler) Run(ctx context.Context) {
	ticker := time.NewTicker(metricsFlushInterval)
	defer ticker.Stop()

	var pending []model.MetricRecord

	flush := func() {
		if len(pending) == 0 {
			return
		}
		if err := h.repo.InsertMetrics(context.Background(), pending); err != nil {
			h.logger.Error("metrics insert failed", "error", err, "count", len(pending))
		}
		pending = pending[:0]
	}

	for {
		select {
		case recs := <-h.ch:
			pending = append(pending, recs...)
			if len(pending) >= metricsFlushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// Drain buffered batches before returning.
			for {
				select {
				case recs := <-h.ch:
					pending = append(pending, recs...)
				default:
					flush()
					return
				}
			}
		}
	}
}
