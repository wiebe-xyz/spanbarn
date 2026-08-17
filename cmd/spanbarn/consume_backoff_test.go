package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestBackoffAfterConsumeErrorStopsOnCancel is the important one. The metrics
// and logs consumers used to log at ERROR and `continue` immediately, with no
// context check: on shutdown the cancelled context surfaced as an error and
// filed a BugBarn issue for a clean stop (SPA-58, SPA-39). A cancelled context
// must end the loop silently.
func TestBackoffAfterConsumeErrorStopsOnCancel(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if backoffAfterConsumeError(ctx, logger, "metrics", context.Canceled) {
		t.Error("returned true for a cancelled context; the consumer would keep looping")
	}
	if out := strings.TrimSpace(logBuf.String()); out != "" {
		t.Errorf("logged during shutdown: %s", out)
	}
}

// TestBackoffAfterConsumeErrorWarnsAndWaits covers the live path: a dropped
// Redis connection is transient and self-healing, so it warns rather than
// errors — and it waits, because the old code spun in a hot loop hammering
// Redis and emitting one captured event per iteration.
func TestBackoffAfterConsumeErrorWarnsAndWaits(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	start := time.Now()
	ok := backoffAfterConsumeError(context.Background(), logger, "logs", errors.New("read tcp: i/o timeout"))
	elapsed := time.Since(start)

	if !ok {
		t.Error("returned false for a live context; the consumer would stop on a transient error")
	}
	if elapsed < consumeErrorBackoff {
		t.Errorf("returned after %v, want at least %v — without a wait this is a hot loop", elapsed, consumeErrorBackoff)
	}

	out := logBuf.String()
	if !strings.Contains(out, "logs consumer") {
		t.Errorf("log line does not identify the consumer: %s", out)
	}
	// WARN, not ERROR: nothing was lost, and ERROR is what files an issue.
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN for a transient, retried failure, got: %s", out)
	}
}
