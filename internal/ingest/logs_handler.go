package ingest

import (
	"context"
	"log/slog"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const (
	logsChannelSize   = 256
	logsFlushSize     = 500
	logsFlushInterval = 100 * time.Millisecond
)

// LogsRepository is the write-side subset needed by LogsHandler.
type LogsRepository interface {
	InsertLogs(ctx context.Context, recs []model.LogRecord) error
}

// LogsHandler buffers incoming OTLP log records and writes them to SQLite in
// batches. No spool — losing a flush on crash is acceptable.
type LogsHandler struct {
	ch     chan []model.LogRecord
	repo   LogsRepository
	logger *slog.Logger
}

// NewLogsHandler creates a LogsHandler ready to call Run.
func NewLogsHandler(repo LogsRepository, logger *slog.Logger) *LogsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogsHandler{
		ch:     make(chan []model.LogRecord, logsChannelSize),
		repo:   repo,
		logger: logger,
	}
}

// Enqueue adds a batch of log records to the in-memory channel.
// If the channel is full the batch is dropped and a warning is logged.
func (h *LogsHandler) Enqueue(recs []model.LogRecord) {
	if len(recs) == 0 {
		return
	}
	select {
	case h.ch <- recs:
	default:
		h.logger.Warn("logs channel full, dropping batch", "count", len(recs))
	}
}

// Run drains the channel and writes batches to the repository until ctx is
// cancelled. After cancellation it flushes remaining buffered records before
// returning. Blocks until done — intended for use with safeGo.
func (h *LogsHandler) Run(ctx context.Context) {
	ticker := time.NewTicker(logsFlushInterval)
	defer ticker.Stop()

	var pending []model.LogRecord

	flush := func() {
		if len(pending) == 0 {
			return
		}
		if err := h.repo.InsertLogs(context.Background(), pending); err != nil {
			h.logger.Error("logs insert failed", "error", err, "count", len(pending))
		}
		pending = pending[:0]
	}

	for {
		select {
		case recs := <-h.ch:
			pending = append(pending, recs...)
			if len(pending) >= logsFlushSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
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
