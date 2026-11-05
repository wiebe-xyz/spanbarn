package ingest

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

var errRedisFull = errors.New("queue: lpush: OOM command not allowed when used memory > 'maxmemory'")

// failingStore fails every insert and signals each attempt.
type failingStore struct{ calls chan struct{} }

func newFailingStore(n int) *failingStore {
	return &failingStore{calls: make(chan struct{}, n)}
}

func (s *failingStore) InsertLogs(context.Context, []model.LogRecord) error {
	s.calls <- struct{}{}
	return errRedisFull
}

func (s *failingStore) InsertMetrics(context.Context, []model.MetricRecord) error {
	s.calls <- struct{}{}
	return errRedisFull
}

var countAttr = regexp.MustCompile(`count=(\d+)`)

// assertCappedFailureLog: the first failure logs at once, shutdown logs the rest,
// and the two lines together hold every record lost.
func assertCappedFailureLog(t *testing.T, logs string, wantRecords int) {
	t.Helper()
	if got := strings.Count(logs, "level=ERROR"); got != 2 {
		t.Fatalf("ERROR lines = %d, want 2:\n%s", got, logs)
	}
	total := 0
	for _, m := range countAttr.FindAllStringSubmatch(logs, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != wantRecords {
		t.Errorf("records on the lines = %d, want %d:\n%s", total, wantRecords, logs)
	}
}

// runUntilFlushes runs a handler until it has failed the given number of flushes,
// then stops it and waits for Run to return.
func runUntilFlushes(t *testing.T, store *failingStore, flushes int, run func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	for i := 0; i < flushes; i++ {
		select {
		case <-store.calls:
		case <-time.After(30 * time.Second):
			t.Fatalf("flush %d never happened", i)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestLogsHandlerCapsTheFailedFlushLine(t *testing.T) {
	const flushes = 20
	var logs bytes.Buffer
	store := newFailingStore(flushes)
	h := NewLogsHandler(store, slog.New(slog.NewTextHandler(&logs, nil)))
	for i := 0; i < flushes; i++ {
		h.Enqueue(make([]model.LogRecord, logsFlushSize))
	}
	runUntilFlushes(t, store, flushes, h.Run)
	assertCappedFailureLog(t, logs.String(), flushes*logsFlushSize)
}

func TestMetricsHandlerCapsTheFailedFlushLine(t *testing.T) {
	const flushes = 20
	var logs bytes.Buffer
	store := newFailingStore(flushes)
	h := NewMetricsHandler(store, slog.New(slog.NewTextHandler(&logs, nil)))
	for i := 0; i < flushes; i++ {
		h.Enqueue(make([]model.MetricRecord, metricsFlushSize))
	}
	runUntilFlushes(t, store, flushes, h.Run)
	assertCappedFailureLog(t, logs.String(), flushes*metricsFlushSize)
}

// The rest goes out once the interval has passed, also with no new failure.
func TestFlushFailuresReportAfterTheInterval(t *testing.T) {
	var logs bytes.Buffer
	f := flushFailures{logger: slog.New(slog.NewTextHandler(&logs, nil)), msg: "insert failed"}
	start := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)

	f.add(3, errRedisFull, start)
	f.add(4, errRedisFull, start.Add(time.Second))
	f.add(5, errRedisFull, start.Add(flushFailureReportInterval-time.Second))
	if got := strings.Count(logs.String(), "level=ERROR"); got != 1 {
		t.Fatalf("ERROR lines within the interval = %d, want 1:\n%s", got, logs.String())
	}

	logs.Reset()
	f.report(start.Add(flushFailureReportInterval), false)
	if !strings.Contains(logs.String(), "flushes=2 count=9") {
		t.Errorf("line does not hold the total since the last one:\n%s", logs.String())
	}

	logs.Reset()
	f.report(start.Add(2*flushFailureReportInterval), true)
	if logs.Len() != 0 {
		t.Errorf("logged with nothing to report:\n%s", logs.String())
	}
}
