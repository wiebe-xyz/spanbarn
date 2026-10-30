package retention

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// failingWorker returns a worker whose database is closed, so every cycle
// fails, and the buffer its log lines go to.
func failingWorker(t *testing.T) (*RetentionWorker, *bytes.Buffer) {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateSingleFile(context.Background(), db.DB); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewRepository(db.DB)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	worker := NewRetentionWorker(repo, Config{BatchYield: time.Millisecond}, logger)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return worker, &buf
}

// A cycle that keeps failing is logged at error level once, so selflog files it
// in BugBarn; the attempts before it stay below error.
func TestRunWithRetryLogsFinalFailureAtError(t *testing.T) {
	worker, buf := failingWorker(t)

	var pauses []time.Duration
	worker.runWithRetry(context.Background(), func(d time.Duration) { pauses = append(pauses, d) })

	out := buf.String()
	if got := strings.Count(out, `"level":"ERROR"`); got != 1 {
		t.Fatalf("%d error lines, want 1:\n%s", got, out)
	}
	if got := strings.Count(out, `"msg":"retention cycle attempt failed"`); got != retentionAttempts {
		t.Errorf("%d attempt lines, want %d", got, retentionAttempts)
	}
	if len(pauses) != retentionAttempts {
		t.Errorf("%d pauses, want %d", len(pauses), retentionAttempts)
	}
}

// A shutdown during a cycle is not a failure.
func TestRunWithRetryStaysQuietOnShutdown(t *testing.T) {
	worker, buf := failingWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worker.runWithRetry(ctx, func(time.Duration) {})

	if strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("error logged on shutdown:\n%s", buf.String())
	}
}
