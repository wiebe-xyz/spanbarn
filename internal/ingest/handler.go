package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

var tracer = otel.Tracer("spanbarn/ingest")

// DefaultFlushInterval is the default interval between queue-to-spool flushes.
const DefaultFlushInterval = 5 * time.Millisecond

// Handler ties the in-memory queue to the durable spool.
// It runs a background goroutine that periodically drains the queue
// and writes the records to the spool file.
type Handler struct {
	queue         *Queue
	spool         *spool.Spool
	flushInterval time.Duration
	logger        *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHandler creates a new ingest handler.
func NewHandler(queue *Queue, sp *spool.Spool, flushInterval time.Duration, logger *slog.Logger) *Handler {
	if flushInterval <= 0 {
		flushInterval = DefaultFlushInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		queue:         queue,
		spool:         sp,
		flushInterval: flushInterval,
		logger:        logger,
	}
}

// Start begins the background flush goroutine.
func (h *Handler) Start(ctx context.Context) {
	ctx, h.cancel = context.WithCancel(ctx)
	h.wg.Add(1)
	go h.flushLoop(ctx)
}

// Enqueue delegates to the underlying queue.
func (h *Handler) Enqueue(record model.SpanRecord) bool {
	return h.queue.Enqueue(record)
}

// Stop cancels the background goroutine and flushes remaining items.
func (h *Handler) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
	h.wg.Wait()

	// Final flush of any remaining items.
	h.flush()
}

func (h *Handler) flushLoop(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(h.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.flush()
		}
	}
}

func (h *Handler) flush() {
	records := h.queue.Drain()
	if len(records) == 0 {
		return
	}
	_, span := tracer.Start(context.Background(), "ingest.flush_to_spool")
	span.SetAttributes(attribute.Int("record_count", len(records)))
	err := h.spool.Write(records)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		h.logger.Error("failed to flush queue to spool", "count", len(records), "error", err)
	}
	span.End()
}
