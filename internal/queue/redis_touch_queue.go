package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	// TouchQueueKey carries api_keys.id values whose last_used_at the writer
	// should bump.
	TouchQueueKey       = "spanbarn:apikey-touch-queue"
	touchQueueBatchSize = 100

	// touchPublishTimeout bounds how long an authenticating request can wait on
	// Redis. Authorize ignores the touch result, so a slow or dead Redis must
	// cost a bounded pause rather than hanging auth.
	touchPublishTimeout = time.Second

	// DefaultTouchInterval is how stale last_used_at is allowed to be. Authorize
	// runs on every request, so touches are coalesced to at most one publish per
	// key per interval — "when was this key last used" needs minutes, not
	// milliseconds, and one LPUSH per request would be absurd.
	DefaultTouchInterval = time.Minute
)

// PublishAPIKeyTouches LPUSHes api-key IDs onto the touch queue.
func (q *RedisQueue) PublishAPIKeyTouches(ctx context.Context, ids []int64) error {
	return publishBatch(ctx, q, TouchQueueKey, "apikey-touch", touchQueueBatchSize, ids)
}

// ConsumeAPIKeyTouches blocks for up to brpopTimeout waiting for a batch of
// api-key IDs. Returns nil, nil when the queue is empty. Called by the writer's
// touch consumer goroutine.
func (q *RedisQueue) ConsumeAPIKeyTouches(ctx context.Context) ([]int64, error) {
	return consumeBatch[int64](ctx, q, TouchQueueKey, "apikey-touch")
}

// TouchPublisher records api-key usage from a pod that cannot write.
//
// The reader and ingest pods open SQLite read-only, so they previously dropped
// every touch on the floor and last_used_at was permanently NULL. The column
// was supposed to be inferable from the presence of spans, but tail sampling
// keeps only 1 trace in 1000 by default: a busy key can legitimately produce no
// spans at all, making a live key and a revoked-in-all-but-name key look
// identical. That inference cost real time during the 2026-07-13 key outage.
//
// Publishing through the write queue keeps the read-only invariant intact — the
// reader only enqueues; the writer performs the UPDATE.
type TouchPublisher struct {
	queue    *RedisQueue
	interval time.Duration
	logger   *slog.Logger

	mu   sync.Mutex
	seen map[int64]time.Time
	now  func() time.Time // injectable for tests
}

// NewTouchPublisher returns a TouchPublisher that coalesces touches per key to
// at most one publish per interval.
func NewTouchPublisher(q *RedisQueue, interval time.Duration, logger *slog.Logger) *TouchPublisher {
	if interval <= 0 {
		interval = DefaultTouchInterval
	}
	return &TouchPublisher{
		queue:    q,
		interval: interval,
		logger:   logger,
		seen:     make(map[int64]time.Time),
		now:      time.Now,
	}
}

// TouchAPIKey enqueues a last_used_at bump for id, coalesced to one publish per
// key per interval.
//
// It always reports success: a failed touch must never fail authentication over
// a diagnostic column. Failures are logged instead.
func (p *TouchPublisher) TouchAPIKey(id int64) error {
	if !p.claim(id) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), touchPublishTimeout)
	defer cancel()
	if err := p.queue.PublishAPIKeyTouches(ctx, []int64{id}); err != nil && p.logger != nil {
		p.logger.Warn("failed to publish api key touch", "id", id, "error", err)
	}
	return nil
}

// claim reports whether id is due to be published, recording the attempt.
func (p *TouchPublisher) claim(id int64) bool {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.seen[id]; ok && now.Sub(last) < p.interval {
		return false
	}
	p.seen[id] = now
	return true
}
