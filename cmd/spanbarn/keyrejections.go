package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// keyRejectionFinalTimeout bounds the last hand-over of a stopping pod.
const keyRejectionFinalTimeout = 5 * time.Second

// startKeyRejectionCounter makes the authorizer count the requests whose key no
// stored key matches and hands the tally to store every
// auth.RejectionFlushInterval until ctx ends, then once more. A pod that cannot
// write stores through the write queue, the writer and standalone pods write
// the table themselves (#572).
func startKeyRejectionCounter(ctx context.Context, wg *sync.WaitGroup, authorizer *auth.Authorizer,
	store func(context.Context, []model.KeyRejection) error, logger *slog.Logger) {
	counter := auth.NewRejectionCounter()
	authorizer.SetRejectionSink(counter)
	safeGo("key-rejections", wg, func() {
		counter.Run(ctx, auth.RejectionFlushInterval, store, logger)
		final, cancel := context.WithTimeout(context.Background(), keyRejectionFinalTimeout)
		defer cancel()
		counter.FlushTo(final, store, logger)
	})
}

// keyRejectionSink is the table side: *repository.Repository.
type keyRejectionSink interface {
	AddKeyRejections(rows []model.KeyRejection, now time.Time) error
}

// storeKeyRejections returns the store func of a pod that owns the database.
func storeKeyRejections(repo keyRejectionSink) func(context.Context, []model.KeyRejection) error {
	return func(_ context.Context, rows []model.KeyRejection) error {
		return repo.AddKeyRejections(rows, time.Now())
	}
}

// keyRejectionSource is the queue side of runKeyRejectionConsumer.
type keyRejectionSource interface {
	ConsumeKeyRejections(ctx context.Context) ([]model.KeyRejection, error)
}

// runKeyRejectionConsumer adds the counts reader and ingest pods publish to the
// table. Like the touch queue it is a diagnostic tally: a failed write is
// logged and the batch is dropped, so the single write connection is not held
// by a retry loop.
func runKeyRejectionConsumer(ctx context.Context, q keyRejectionSource, repo keyRejectionSink, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		rows, err := q.ConsumeKeyRejections(ctx)
		if err != nil {
			if !backoffAfterConsumeError(ctx, logger, "key rejection", err) {
				return
			}
			continue
		}
		if len(rows) == 0 {
			continue
		}
		if err := repo.AddKeyRejections(rows, time.Now()); err != nil {
			logger.Warn("key rejections: store failed, dropping the batch", "keys", len(rows), "error", err)
		}
	}
}
