package forward

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/observability/bugbarntest"
)

// The cap holds back later lines in a minute and never the first: the first
// drop logs at ERROR at once and files a BugBarn event that names it.
func TestRedisForwarderFirstDropReachesBugBarn(t *testing.T) {
	f, _, _ := droppingForwarder(t, 6*defaultBatchSize)
	logger, bugbarn := bugbarntest.Logger(t, slog.NewTextHandler(io.Discard, nil))
	f.logger = logger

	cursor := int64(0)
	for i := 0; i < 6; i++ {
		next, _, err := f.forwardBatch(context.Background(), cursor)
		if err != nil {
			t.Fatalf("forwardBatch %d: %v", i, err)
		}
		cursor = next
	}

	events := bugbarn.Events()
	if len(events) != 1 {
		t.Fatalf("BugBarn received %d events within the minute, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Level != "ERROR" || ev.Message != "redis forwarder: queue full, dropped batches" {
		t.Errorf("event %q at %s, want the queue-full drop at ERROR", ev.Message, ev.Level)
	}
	if ev.Attributes["batches"] != float64(1) || ev.Attributes["spans"] != float64(defaultBatchSize) {
		t.Errorf("event attributes %v, want the first batch of %d spans", ev.Attributes, defaultBatchSize)
	}
}
