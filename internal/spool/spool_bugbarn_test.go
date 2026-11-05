package spool

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/observability/bugbarntest"
)

// The first undecodable line files a BugBarn event as soon as the reader moves
// past it; the cap holds back only the lines after it in the same minute.
func TestFirstUndecodableLineReachesBugBarn(t *testing.T) {
	logger, bugbarn := bugbarntest.Logger(t, slog.NewTextHandler(io.Discard, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	sp, err := NewSpool(dir, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, spoolFileName)
	cursor := int64(0)
	for i := 0; i < 3; i++ {
		appendTornLine(t, path)
		if err := sp.Write(makeRecords(1)); err != nil {
			t.Fatal(err)
		}
		_, next, err := sp.Read(cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if err := sp.SaveCursor(next); err != nil {
			t.Fatal(err)
		}
		cursor = next
	}

	events := bugbarn.Events()
	if len(events) != 1 {
		t.Fatalf("BugBarn received %d events within the minute, want 1: %+v", len(events), events)
	}
	ev := events[0]
	if ev.Level != "ERROR" || ev.Message != "spool: skipped undecodable lines" {
		t.Errorf("event %q at %s, want the skipped line at ERROR", ev.Message, ev.Level)
	}
	if ev.Attributes["lines"] != float64(1) || ev.Attributes["total"] != float64(1) {
		t.Errorf("event attributes %v, want the first line", ev.Attributes)
	}
}
