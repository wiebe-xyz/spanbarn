package auth

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrKeyNotFound is what a KeyLookup returns for a hash no key has. Any other
// lookup error (a busy database) is not a verdict on the key and is not counted.
var ErrKeyNotFound = errors.New("api key not found")

// RejectionSink is told about every request made with a key that no stored key
// matches. fingerprint is the first KeyFingerprintLen hex digits of the key's
// hash.
type RejectionSink interface {
	Reject(fingerprint string)
}

// KeyFingerprintLen is the number of hex digits of a key hash that name the key
// in the rejection counters.
const KeyFingerprintLen = 8

// rejectionMaxKeys bounds the fingerprints held between two flushes. A sender
// that tries a new key on every request must not grow the map without limit.
const rejectionMaxKeys = 1024

// KeyRejection counts the requests one unknown or revoked API key made.
// Fingerprint is the first eight hex digits of the key's SHA-256 hash: enough
// to match a key a sender still uses, and of no use for guessing it. LastSeen is
// the unix second of the newest rejected request. The auth package is a leaf, so
// the pod wiring converts it to model.KeyRejection for the queue and the table.
type KeyRejection struct {
	Fingerprint string
	Count       int64
	LastSeen    int64
}

// RejectionCounter tallies rejected keys in memory until Flush takes them. It
// is the RejectionSink of every pod; the pod decides where the tally goes.
type RejectionCounter struct {
	now func() time.Time

	mu      sync.Mutex
	counts  map[string]*KeyRejection
	dropped int64
}

// NewRejectionCounter returns an empty counter.
func NewRejectionCounter() *RejectionCounter {
	return &RejectionCounter{now: time.Now, counts: make(map[string]*KeyRejection)}
}

// Reject counts one rejected request.
func (c *RejectionCounter) Reject(fingerprint string) {
	if c == nil || fingerprint == "" {
		return
	}
	now := c.now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.counts[fingerprint]
	if e == nil {
		if len(c.counts) >= rejectionMaxKeys {
			c.dropped++
			return
		}
		e = &KeyRejection{Fingerprint: fingerprint}
		c.counts[fingerprint] = e
	}
	e.Count++
	e.LastSeen = now
}

// Flush returns the counts since the last flush and the number of requests
// dropped for lack of room, and starts a new tally.
func (c *RejectionCounter) Flush() (rows []KeyRejection, dropped int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.counts {
		rows = append(rows, *e)
	}
	dropped = c.dropped
	c.counts = make(map[string]*KeyRejection)
	c.dropped = 0
	return rows, dropped
}

// RejectionFlushInterval is how often a pod hands its tally on. A rejected key
// shows in the counters within one interval plus the queue and the writer.
const RejectionFlushInterval = 15 * time.Second

// Run hands the tally to store every interval until ctx ends, then once more.
// A failed store puts the counts back for the next interval.
func (c *RejectionCounter) Run(ctx context.Context, interval time.Duration, store func(context.Context, []KeyRejection) error, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.flushTo(ctx, store, logger)
		}
	}
}

// FlushTo is one flush: it hands the tally to store and puts it back on failure.
func (c *RejectionCounter) FlushTo(ctx context.Context, store func(context.Context, []KeyRejection) error, logger *slog.Logger) {
	c.flushTo(ctx, store, logger)
}

func (c *RejectionCounter) flushTo(ctx context.Context, store func(context.Context, []KeyRejection) error, logger *slog.Logger) {
	rows, dropped := c.Flush()
	if dropped > 0 {
		logger.Warn("key rejections: too many distinct keys between flushes, requests not counted", "requests", dropped)
	}
	if len(rows) == 0 {
		return
	}
	if err := store(ctx, rows); err != nil {
		logger.Warn("key rejections: store failed, keeping the counts for the next flush", "keys", len(rows), "error", err)
		c.restore(rows)
	}
}

// restore adds counts a failed store did not take back to the tally.
func (c *RejectionCounter) restore(rows []KeyRejection) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		e := c.counts[r.Fingerprint]
		if e == nil {
			if len(c.counts) >= rejectionMaxKeys {
				c.dropped += r.Count
				continue
			}
			cp := r
			c.counts[r.Fingerprint] = &cp
			continue
		}
		e.Count += r.Count
		if r.LastSeen > e.LastSeen {
			e.LastSeen = r.LastSeen
		}
	}
}
