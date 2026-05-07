package forward

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

var tracer = otel.Tracer("spanbarn/forward")

const (
	defaultBatchSize    = 500
	defaultTickInterval = 1 * time.Second
	maxRetryDelay       = 30 * time.Second
)

// Forwarder reads SpanRecords from a spool and POSTs them to a writer endpoint.
type Forwarder struct {
	spool     *spool.Spool
	writerURL string
	apiKey    string
	client    *http.Client
	logger    *slog.Logger
}

// New creates a Forwarder that reads from sp and forwards to writerURL.
func New(sp *spool.Spool, writerURL, apiKey string, logger *slog.Logger) *Forwarder {
	return &Forwarder{
		spool:     sp,
		writerURL: writerURL,
		apiKey:    apiKey,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}
}

// Run starts the forwarding loop until ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context) {
	cursor, err := f.spool.LoadCursor()
	if err != nil {
		f.logger.Warn("failed to load cursor, starting from 0", "error", err)
	}

	ticker := time.NewTicker(defaultTickInterval)
	defer ticker.Stop()

	retryDelay := time.Duration(0)

	for {
		select {
		case <-ctx.Done():
			f.logger.Info("forwarder stopped")
			return
		case <-ticker.C:
			if retryDelay > 0 {
				time.Sleep(retryDelay)
			}

			records, newCursor, err := f.spool.Read(cursor, defaultBatchSize)
			if err != nil {
				f.logger.Error("spool read failed", "error", err)
				continue
			}
			if len(records) == 0 {
				retryDelay = 0
				continue
			}

			if err := f.send(ctx, records); err != nil {
				f.logger.Error("forward failed, will retry", "error", err, "count", len(records))
				retryDelay = min(retryDelay*2+time.Second, maxRetryDelay)
				continue
			}

			retryDelay = 0
			cursor = newCursor
			if err := f.spool.SaveCursor(cursor); err != nil {
				f.logger.Error("failed to save cursor", "error", err)
			}
		}
	}
}

func (f *Forwarder) send(ctx context.Context, records []model.SpanRecord) error {
	ctx, span := tracer.Start(ctx, "forward.send")
	span.SetAttributes(attribute.Int("record_count", len(records)))
	defer span.End()

	body, err := json.Marshal(records)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.writerURL+"/internal/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if f.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.apiKey)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		err := fmt.Errorf("writer returned %d", resp.StatusCode)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	return nil
}
