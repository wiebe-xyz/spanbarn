package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/tier"
)

// logLine is one JSON log line of the worker.
type logLine struct {
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	Ladder string `json:"ladder"`
	Tier   string `json:"tier"`
}

func logLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var out []logLine
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var l logLine
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("log line %q: %v", raw, err)
		}
		out = append(out, l)
	}
	return out
}

// stallErrors returns "ladder/tier" of each stall reported at error level.
func stallErrors(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var out []string
	for _, l := range logLines(t, buf) {
		if l.Level == "ERROR" && strings.HasPrefix(l.Msg, "rollup compaction stalled") {
			out = append(out, l.Ladder+"/"+l.Tier)
		}
	}
	return out
}

// loggedWorker is a test worker that logs JSON into the returned buffer.
func loggedWorker(t *testing.T) (*RetentionWorker, *repository.Repository, *bytes.Buffer) {
	t.Helper()
	w, repo := setupTestWorker(t, Config{})
	buf := &bytes.Buffer{}
	w.logger = slog.New(slog.NewJSONHandler(buf, nil))
	return w, repo, buf
}

// mark sets a watermark or fails the test.
func mark(t *testing.T, set func(context.Context, int64, time.Time) error, step int64, at time.Time) {
	t.Helper()
	if err := set(context.Background(), step, at); err != nil {
		t.Fatalf("set watermark: %v", err)
	}
}

// A compactor that stops leaves its watermarks behind, and nothing else
// reports it. The retention cycle reports each tier that owes too many closed
// buckets at error level, on both ladders.
func TestRetentionCycleReportsStalledTiers(t *testing.T) {
	w, repo, buf := loggedWorker(t)
	hour := tier.TruncateBucket(time.Now().UTC(), tier.StepHour)
	day := tier.TruncateBucket(time.Now().UTC(), tier.StepDay)
	mark(t, repo.SetMetricRollupWatermark, tier.Step5m, hour.Add(-6*time.Hour))  // metric hourly: stalled
	mark(t, repo.SetMetricRollupWatermark, tier.StepHour, day.AddDate(0, 0, -4)) // metric daily: stalled
	mark(t, repo.SetSpanRollupWatermark, tier.Step5m, hour.Add(-6*time.Hour))    // span hourly: stalled
	mark(t, repo.SetSpanRollupWatermark, tier.StepHour, day)                     // span daily: current

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	got := stallErrors(t, buf)
	want := []string{"metric/hourly", "metric/daily", "span/hourly"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stalls reported %v, want %v", got, want)
	}
}

// A tier inside its threshold is a compactor at work, and logs nothing.
func TestRetentionCycleQuietWhileCompactionKeepsUp(t *testing.T) {
	w, repo, buf := loggedWorker(t)
	now := time.Now().UTC()
	// One hour short of the newest closed hour, and the day before yesterday.
	mark(t, repo.SetMetricRollupWatermark, tier.Step5m, tier.TruncateBucket(now.Add(-90*time.Minute), tier.StepHour))
	mark(t, repo.SetMetricRollupWatermark, tier.StepHour, tier.TruncateBucket(now.AddDate(0, 0, -1), tier.StepDay))

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := stallErrors(t, buf); len(got) != 0 {
		t.Errorf("stalls reported %v while compaction keeps up", got)
	}
}
