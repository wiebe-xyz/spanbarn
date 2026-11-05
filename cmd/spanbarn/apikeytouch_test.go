package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// failingTouchSource fails every consume. It cancels the consumer's context
// once it has been called limit times, so a loop that never backs off still
// ends.
type failingTouchSource struct {
	mu     sync.Mutex
	at     []time.Time
	limit  int
	cancel context.CancelFunc
	err    func(ctx context.Context) error
}

func (s *failingTouchSource) ConsumeAPIKeyTouches(ctx context.Context) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = append(s.at, time.Now())
	if len(s.at) >= s.limit {
		s.cancel()
	}
	return nil, s.err(ctx)
}

type noTouchSink struct{}

func (noTouchSink) TouchAPIKey(int64) error { return nil }

// TestTouchConsumerBacksOffOnConsumeError covers SPA-90: a DNS timeout on the
// touch queue logged at ERROR, which files a BugBarn issue, and the loop
// retried at once. During a Redis outage that is a hot loop with one captured
// error per pass. A failed consume warns and waits like the other consumers.
func TestTouchConsumerBacksOffOnConsumeError(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &failingTouchSource{limit: 2, cancel: cancel, err: func(context.Context) error {
		return errors.New("queue: brpop apikey-touch: dial tcp: lookup spanbarn-redis-queue: i/o timeout")
	}}

	runTouchConsumer(ctx, src, noTouchSink{}, logger)

	// The source cancels on its second consume, so the loop ends after it;
	// the gap between the two consumes is the wait, however slow the host.
	if len(src.at) != 2 {
		t.Fatalf("consumed %d times, want 2", len(src.at))
	}
	if gap := src.at[1].Sub(src.at[0]); gap < consumeErrorBackoff {
		t.Errorf("retried %v after a failed consume, want at least %v; without a wait this is a hot loop", gap, consumeErrorBackoff)
	}
	out := logBuf.String()
	if n := strings.Count(out, "level=ERROR"); n > 0 {
		t.Errorf("a transient consume failure logged %d lines at ERROR, which files a BugBarn issue", n)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Error("a failed consume should log a warning")
	}
}

// TestTouchConsumerStopsQuietlyOnShutdown: on a clean stop the consume returns
// the cancelled context's error, and the loop must end without logging it.
func TestTouchConsumerStopsQuietlyOnShutdown(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &failingTouchSource{limit: 1, cancel: cancel, err: func(ctx context.Context) error {
		return ctx.Err()
	}}

	done := make(chan struct{})
	go func() {
		runTouchConsumer(ctx, src, noTouchSink{}, logger)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer did not stop after its context was cancelled")
	}
	if out := strings.TrimSpace(logBuf.String()); out != "" {
		t.Errorf("logged during shutdown: %s", out)
	}
}
