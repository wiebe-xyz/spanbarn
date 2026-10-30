package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func TestSpanLedgerLost(t *testing.T) {
	cases := []struct {
		name    string
		row     model.SpanLedgerRow
		settled bool
		want    int64
	}{
		{"everything accounted for", model.SpanLedgerRow{Accepted: 100, SampledOut: 60, ShedUnsampled: 10, Forwarded: 30, Processed: 30}, true, 0},
		{"pod died with spans buffered", model.SpanLedgerRow{Accepted: 100, SampledOut: 10, Forwarded: 10, Processed: 10}, true, 80},
		{"spans still on their way", model.SpanLedgerRow{Accepted: 100, SampledOut: 10, Forwarded: 10, Processed: 10}, false, 0},
		{"lost after the buffer", model.SpanLedgerRow{Accepted: 100, Forwarded: 100, Processed: 70}, true, 30},
		{"named loss counts before settling", model.SpanLedgerRow{Accepted: 100, LostInBuffer: 4, Failed: 3}, false, 7},
		{"named loss is not counted twice", model.SpanLedgerRow{Accepted: 10, LostInBuffer: 4, Failed: 3, Forwarded: 6, Processed: 3}, true, 7},
		{"last report of a killed pod missing", model.SpanLedgerRow{Accepted: 100, Forwarded: 90, Processed: 100}, true, 0},
		{"writer spans with no reader count", model.SpanLedgerRow{Processed: 50}, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := spanLedgerLost(c.row, c.settled); got != c.want {
				t.Errorf("lost = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSpanLedgerServiceMarksSettledBuckets(t *testing.T) {
	repo := setupTestRepo(t)
	now := time.Now()
	old := model.SpanLedgerBucket(now.Add(-2 * time.Hour).UnixMicro())
	young := model.SpanLedgerBucket(now.UnixMicro())
	if err := repo.AddSpanLedgerRows([]model.SpanLedgerRow{
		{Bucket: old, ProjectID: 1, Accepted: 10, Processed: 4},
		{Bucket: young, ProjectID: 1, Accepted: 10, Processed: 4},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewQueryService(repo, nil, nil)
	got, err := svc.SpanLedger(context.Background(), 1, now.Add(-3*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %+v, want 2", got)
	}
	if !got[0].Settled || got[0].LostSpans != 6 {
		t.Errorf("old bucket = %+v, want settled with 6 lost", got[0])
	}
	if got[1].Settled || got[1].LostSpans != 0 {
		t.Errorf("young bucket = %+v, want unsettled with none lost", got[1])
	}
}

func TestAttachWindowLoss(t *testing.T) {
	repo := setupTestRepo(t)
	now := time.Now()
	if err := repo.AddSpanLedgerRows([]model.SpanLedgerRow{
		{Bucket: model.SpanLedgerBucket(now.Add(-2 * time.Hour).UnixMicro()), ProjectID: 7, Accepted: 10, Processed: 4},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewQueryService(repo, nil, nil)
	from, to := now.Add(-3*time.Hour), now

	summaries := []PromptSummary{{Name: "a"}, {Name: "b"}}
	svc.attachWindowLoss(context.Background(), summaries, 7, from, to)
	for _, s := range summaries {
		if s.WindowLostSpans == nil || *s.WindowLostSpans != 6 {
			t.Errorf("summary %s: lost = %v, want 6", s.Name, s.WindowLostSpans)
		}
	}

	allProjects := []PromptSummary{{Name: "a"}}
	svc.attachWindowLoss(context.Background(), allProjects, 0, from, to)
	if allProjects[0].WindowLostSpans != nil {
		t.Error("summary over all projects carries a loss, want none")
	}
}

// fakeLedgerReader serves rows and records the range it was asked for.
type fakeLedgerReader struct {
	rows     []model.SpanLedgerRow
	err      error
	from, to time.Time
	calls    int
}

func (f *fakeLedgerReader) ListSpanLedger(_ context.Context, _ int64, from, to time.Time) ([]model.SpanLedgerRow, error) {
	f.calls++
	f.from, f.to = from, to
	return f.rows, f.err
}

func newLedgerWatch(repo SpanLedgerReader, backlog func(context.Context) (int64, error), at *time.Time) (*SpanLedgerWatch, *bytes.Buffer) {
	var logs bytes.Buffer
	w := NewSpanLedgerWatch(repo, backlog, slog.New(slog.NewTextHandler(&logs, nil)))
	w.now = func() time.Time { return *at }
	w.watermark = w.settledUpTo()
	return w, &logs
}

func noBacklog(context.Context) (int64, error) { return 0, nil }

func TestSpanLedgerWatchLogsLossOfSettledBuckets(t *testing.T) {
	at := time.Unix(1_800_003_000, 0)
	repo := &fakeLedgerReader{rows: []model.SpanLedgerRow{
		{Bucket: 1_800_001_200, ProjectID: 81, Accepted: 100, Processed: 40},
		{Bucket: 1_800_001_200, ProjectID: 5, Accepted: 100, Processed: 60},
	}}
	w, logs := newLedgerWatch(repo, noBacklog, &at)

	w.Check(context.Background())
	if repo.calls != 0 {
		t.Fatalf("read with nothing newly settled: %d calls", repo.calls)
	}

	at = at.Add(5 * time.Minute)
	w.Check(context.Background())
	if repo.calls != 1 || !repo.to.After(repo.from) {
		t.Fatalf("calls = %d, range %v to %v", repo.calls, repo.from, repo.to)
	}
	out := logs.String()
	if !strings.Contains(out, "project_id=81") || !strings.Contains(out, "lost_spans=60") {
		t.Errorf("loss of 60 not logged: %s", out)
	}
	if strings.Contains(out, "project_id=5 ") {
		t.Errorf("loss of 40 is below the threshold and was logged: %s", out)
	}

	w.Check(context.Background())
	if repo.calls != 1 {
		t.Errorf("bucket read again: %d calls", repo.calls)
	}
}

func TestSpanLedgerWatchWaitsWhenItCannotJudge(t *testing.T) {
	cases := []struct {
		name    string
		backlog func(context.Context) (int64, error)
		readErr error
	}{
		{"writer behind", func(context.Context) (int64, error) { return spanLedgerMaxBacklog + 1, nil }, nil},
		{"depth unavailable", func(context.Context) (int64, error) { return 0, errors.New("redis down") }, nil},
		{"read failed", noBacklog, errors.New("no such table")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := time.Unix(1_800_003_000, 0)
			repo := &fakeLedgerReader{err: c.readErr}
			w, _ := newLedgerWatch(repo, c.backlog, &at)
			before := w.watermark
			at = at.Add(5 * time.Minute)
			w.Check(context.Background())
			if !w.watermark.Equal(before) {
				t.Errorf("watermark moved from %v to %v", before, w.watermark)
			}
		})
	}
}

func TestSpanLedgerWatchRunStopsWithContext(t *testing.T) {
	at := time.Unix(1_800_003_000, 0)
	w, _ := newLedgerWatch(&fakeLedgerReader{}, noBacklog, &at)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
}
