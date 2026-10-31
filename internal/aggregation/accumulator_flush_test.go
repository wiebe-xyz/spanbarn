package aggregation

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// failingAggWriter stores nothing while fail is set. With writeFirst set it fails
// like the repository does part way through a batch: it stores that many rows and
// reports the rest as unwritten.
type failingAggWriter struct {
	mockAccWriter
	fail       bool
	writeFirst int
}

func (f *failingAggWriter) UpsertAggregates(aggs []repository.Aggregate) error {
	if !f.fail {
		return f.mockAccWriter.UpsertAggregates(aggs)
	}
	if f.writeFirst == 0 {
		return errors.New("write failed")
	}
	_ = f.mockAccWriter.UpsertAggregates(aggs[:f.writeFirst])
	return &repository.UnwrittenRowsError[repository.Aggregate]{Rows: aggs[f.writeFirst:], Err: errors.New("write failed")}
}

func flushTestAccumulator(w AggregateWriter, now *time.Time) (*Accumulator, *bytes.Buffer) {
	var logs bytes.Buffer
	acc := NewAccumulator(w, time.Minute, time.Hour, slog.New(slog.NewTextHandler(&logs, nil)))
	acc.now = func() time.Time { return *now }
	return acc, &logs
}

func flushTestSpan(name string, at time.Time, durationUs int64) repository.Span {
	return repository.Span{ProjectID: 1, Service: "svc", Name: name, Kind: "server", StartTimeUs: at.UnixMicro(), DurationUs: durationUs}
}

func TestAccumulatorRetriesAFailedFlush(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 30, 0, time.UTC)
	w := &failingAggWriter{fail: true}
	acc, logs := flushTestAccumulator(w, &now)
	acc.Add(flushTestSpan("op", now, 100))

	if err := acc.Flush(context.Background()); err == nil {
		t.Fatal("flush with a failing writer returned nil")
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("a flush that will retry logged at ERROR:\n%s", logs)
	}
	if got := acc.QueryRecent(repository.AggregateFilter{}); len(got) != 1 {
		t.Errorf("QueryRecent while waiting on a retry = %d rows, want 1", len(got))
	}

	acc.Add(flushTestSpan("op", now, 300)) // arrives while the first flush waits
	w.fail = false
	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	got := w.get()
	if len(got) != 1 || got[0].Count != 2 || got[0].SumDurationUs != 400 {
		t.Fatalf("after retry got %+v, want one row of 2 spans summing to 400", got)
	}
}

// The upsert adds on conflict, so the chunks that landed must not go again.
func TestAccumulatorRetriesOnlyTheUnwrittenRows(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 30, 0, time.UTC)
	w := &failingAggWriter{fail: true, writeFirst: 1}
	acc, _ := flushTestAccumulator(w, &now)
	for _, name := range []string{"a", "b", "c"} {
		acc.Add(flushTestSpan(name, now, 100))
	}

	if err := acc.Flush(context.Background()); err == nil {
		t.Fatal("partial flush returned nil")
	}
	w.fail = false
	if err := acc.Flush(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	seen := map[string]int64{}
	for _, row := range w.get() {
		seen[row.Operation] += row.Count
	}
	if len(seen) != 3 || seen["a"] != 1 || seen["b"] != 1 || seen["c"] != 1 {
		t.Fatalf("count per operation = %v, want each at 1", seen)
	}
}

func TestAccumulatorLogsRowsLostPastTheRequeueAge(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 30, 0, time.UTC)
	w := &failingAggWriter{fail: true}
	acc, logs := flushTestAccumulator(w, &now)
	acc.Add(flushTestSpan("old", now.Add(-2*spanRollupRequeueAge), 100))
	acc.Add(flushTestSpan("new", now, 100))

	_ = acc.Flush(context.Background())
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "rows_lost=1") {
		t.Fatalf("drop not logged at ERROR with rows_lost=1:\n%s", logs)
	}
	if len(acc.slots) != 1 {
		t.Fatalf("%d slots kept, want the recent one", len(acc.slots))
	}
}

func TestAccumulatorFlushOnStop(t *testing.T) {
	now := time.Date(2026, 10, 31, 2, 0, 30, 0, time.UTC)
	w := &failingAggWriter{}
	acc, logs := flushTestAccumulator(w, &now)
	acc.Add(flushTestSpan("op", now, 100))
	acc.FlushOnStop(context.Background())
	if len(w.get()) != 1 {
		t.Fatalf("final flush wrote %d rows, want 1", len(w.get()))
	}

	w.fail = true
	acc.Add(flushTestSpan("a", now, 100))
	acc.Add(flushTestSpan("b", now, 100))
	acc.FlushOnStop(context.Background())
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "rows_lost=2") {
		t.Fatalf("failed final flush not logged at ERROR with rows_lost=2:\n%s", logs)
	}
}

// partialRollupWriter fails part way through a batch of each kind, like the
// repository, once.
type partialRollupWriter struct {
	fakeSpanRollupWriter
	fail bool
}

func (w *partialRollupWriter) AddSpanRollups(rows []repository.SpanRollup) error {
	if !w.fail {
		return w.fakeSpanRollupWriter.AddSpanRollups(rows)
	}
	_ = w.fakeSpanRollupWriter.AddSpanRollups(rows[:1])
	return &repository.UnwrittenRowsError[repository.SpanRollup]{Rows: rows[1:], Err: errors.New("write failed")}
}

func (w *partialRollupWriter) AddSpanEdges(rows []repository.SpanEdge) error {
	if !w.fail {
		return w.fakeSpanRollupWriter.AddSpanEdges(rows)
	}
	// The first row is the bucket marker, so the second is the first edge.
	_ = w.fakeSpanRollupWriter.AddSpanEdges(rows[:2])
	return &repository.UnwrittenRowsError[repository.SpanEdge]{Rows: rows[2:], Err: errors.New("write failed")}
}

func TestSpanRollupsRetryOnlyTheUnwrittenRows(t *testing.T) {
	w := &partialRollupWriter{fail: true}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, now)
	for _, name := range []string{"a", "b", "c"} {
		r.Add(repository.Span{ProjectID: 1, Service: "api", Name: name, Kind: "server", Status: "ok", DurationUs: 10, IngestedAt: now})
	}
	if err := r.Flush(); err == nil {
		t.Fatal("partial flush returned nil")
	}
	w.fail = false
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, row := range w.rows {
		seen[row.Name] += row.Count
	}
	if len(seen) != 3 || seen["a"] != 1 || seen["b"] != 1 || seen["c"] != 1 {
		t.Fatalf("count per name = %v, want each at 1", seen)
	}
}

func TestSpanRollupsLogRowsLostPastTheRequeueAge(t *testing.T) {
	w := &fakeSpanRollupWriter{err: errors.New("disk full")}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, now)
	var logs bytes.Buffer
	r.logger = slog.New(slog.NewTextHandler(&logs, nil))
	r.Add(repository.Span{ProjectID: 1, Service: "api", Name: "old", Kind: "server", Status: "ok", DurationUs: 10, IngestedAt: now.Add(-2 * time.Hour)})
	_ = r.Flush()
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "rows_lost=1") {
		t.Fatalf("drop not logged at ERROR with rows_lost=1:\n%s", logs.String())
	}
}

func TestSpanEdgesRetryOnlyTheUnwrittenRows(t *testing.T) {
	w := &partialRollupWriter{fail: true}
	at := time.Date(2026, 10, 26, 12, 7, 30, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, at)
	var spans []repository.Span
	for i, system := range []string{"sqlite", "postgresql", "mysql"} {
		spans = append(spans, repository.Span{ProjectID: 1, TraceID: "t", SpanID: string(rune('a' + i)), Service: "web", Kind: "client",
			Attributes: `{"db.system":"` + system + `"}`, DurationUs: 10, IngestedAt: at})
	}
	r.AddBatch(spans)
	if err := r.Flush(); err == nil {
		t.Fatal("partial flush returned nil")
	}
	w.fail = false
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, edge := range w.edges {
		if edge.Target != "" {
			seen[edge.Target] += edge.Count
		}
	}
	if len(seen) != 3 {
		t.Fatalf("edges per target = %v, want 3 targets", seen)
	}
	for target, count := range seen {
		if count != 1 {
			t.Errorf("edge to %s count = %d, want 1", target, count)
		}
	}
}
