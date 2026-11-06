package ingest

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/bugbarntest"
)

// The first failed flush in a minute files a BugBarn event at once; the cap
// holds back only the ones after it.
func TestFirstFailedFlushReachesBugBarn(t *testing.T) {
	logger, bugbarn := bugbarntest.Logger(t, slog.NewTextHandler(io.Discard, nil))
	f := flushFailures{logger: logger, msg: "logs insert failed"}
	start := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)

	f.add(3, errRedisFull, start)
	f.add(4, errRedisFull, start.Add(time.Second))

	events := bugbarn.Events()
	if len(events) != 1 {
		t.Fatalf("BugBarn received %d events within the minute, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Level != "ERROR" || ev.Message != "logs insert failed" {
		t.Errorf("event %q at %s, want the failed flush at ERROR", ev.Message, ev.Level)
	}
	if ev.Attributes["flushes"] != float64(1) || ev.Attributes["count"] != float64(3) {
		t.Errorf("event attributes %v, want the first flush of 3 records", ev.Attributes)
	}
}
