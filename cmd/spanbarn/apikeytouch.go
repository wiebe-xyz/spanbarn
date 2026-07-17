package main

import (
	"context"
	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// queuedKeyLookupAdapter looks keys up in a read-only repository and sends
// last_used_at bumps to the writer over the write queue, so a pod that cannot
// write still records key usage.
type queuedKeyLookupAdapter struct {
	keyLookupAdapter
	toucher interface{ TouchAPIKey(id int64) error }
}

func (a *queuedKeyLookupAdapter) TouchAPIKey(id int64) error { return a.toucher.TouchAPIKey(id) }

// newReadOnlyKeyLookup builds the KeyLookup used by pods that open SQLite
// read-only (reader, ingest).
//
// Without a write queue there is nowhere to send the touch, so it is dropped
// and last_used_at stays NULL — the same behaviour these pods had before, and
// still preferable to failing auth over a diagnostic column.
func newReadOnlyKeyLookup(roRepo *repository.Repository, writeQueue *queue.RedisQueue, logger *slog.Logger) auth.KeyLookup {
	base := keyLookupAdapter{repo: roRepo}
	if writeQueue == nil {
		return &readOnlyKeyLookupAdapter{base}
	}
	return &queuedKeyLookupAdapter{
		keyLookupAdapter: base,
		toucher:          queue.NewTouchPublisher(writeQueue, queue.DefaultTouchInterval, logger),
	}
}

// runTouchConsumer drains api-key touches published by reader pods and applies
// them. Runs on the writer, which owns the only read-write connection.
//
// Touches are best-effort telemetry about telemetry: a failure is logged and
// the batch moves on rather than retrying, since a stale last_used_at is not
// worth contending for the write connection with actual span ingest.
func runTouchConsumer(ctx context.Context, writeQueue *queue.RedisQueue, repo *repository.Repository, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ids, err := writeQueue.ConsumeAPIKeyTouches(ctx)
		if err != nil {
			logger.Error("apikey touch consumer error", "error", err)
			continue
		}
		for _, id := range ids {
			if err := repo.TouchAPIKey(id); err != nil {
				logger.Warn("apikey touch failed", "id", id, "error", err)
			}
		}
	}
}
