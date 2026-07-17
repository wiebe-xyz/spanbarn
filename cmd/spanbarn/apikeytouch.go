package main

import (
	"context"
	"log/slog"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/queue"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type keyLookupAdapter struct {
	repo *repository.Repository
}

func (a *keyLookupAdapter) GetAPIKeyByHash(keyHash string) (auth.APIKeyRecord, error) {
	k, err := a.repo.GetAPIKeyByHash(keyHash)
	if err != nil {
		return auth.APIKeyRecord{}, err
	}
	return auth.APIKeyRecord{
		ID:        k.ID,
		ProjectID: k.ProjectID,
		Scope:     k.Scope,
	}, nil
}

func (a *keyLookupAdapter) TouchAPIKey(id int64) error {
	return a.repo.TouchAPIKey(id)
}

// readOnlyKeyLookupAdapter serves pods that open the database read-only and
// have no write queue to forward touches over. It reuses keyLookupAdapter's
// GetAPIKeyByHash and drops TouchAPIKey.
//
// Dropping the touch used to be the behaviour everywhere read-only, on the
// grounds that last_used_at was inferable from the spans a key produced. Tail
// sampling keeps 1 trace in 1000 by default, so a perfectly healthy key can
// produce no spans and look identical to a dead one — see newReadOnlyKeyLookup,
// which routes touches through the write queue wherever one exists.
type readOnlyKeyLookupAdapter struct {
	keyLookupAdapter
}

func (a *readOnlyKeyLookupAdapter) TouchAPIKey(_ int64) error { return nil }

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
