package retention

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// The under-5-GiB report is one ERROR per episode: it fires on the first
// reading under the floor, stays quiet while free space stays low or sits
// between the floor and the clear mark, and fires again after a recovery.
func TestReportLowFreeFiresOncePerEpisode(t *testing.T) {
	var buf bytes.Buffer
	w, _ := setupTestWorker(t, Config{InterestingRetentionHours: 48})
	w.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	reports := func() int { return strings.Count(buf.String(), "under 5 GiB") }
	read := func(free int64) {
		w.reportLowFree(engine.Space{VolumeBytes: 24 << 30, VolumeFreeBytes: free})
	}

	read(17 << 30)
	read(4 << 30)
	read(3 << 30)
	read(5<<30 + 1)
	if reports() != 1 {
		t.Fatalf("%d reports in one episode, want 1", reports())
	}
	read(7 << 30)
	read(4 << 30)
	if reports() != 2 {
		t.Fatalf("%d reports after a recovery and a new dip, want 2", reports())
	}
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Error("report is not at ERROR, so it would not reach BugBarn")
	}
}

// A volume that could not be sized reports nothing.
func TestReportLowFreeSkipsUnsizedVolume(t *testing.T) {
	var buf bytes.Buffer
	w, _ := setupTestWorker(t, Config{InterestingRetentionHours: 48})
	w.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	w.reportLowFree(engine.Space{})
	if buf.Len() != 0 {
		t.Errorf("unsized volume logged %q", buf.String())
	}
}
