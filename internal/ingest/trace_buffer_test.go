package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func span(traceID, spanID, parentID, name, status string) model.SpanRecord {
	return model.SpanRecord{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentID,
		Name:         name,
		Status:       status,
		ProjectID:    1,
	}
}

func TestTraceBuffer_ErrorTraceAlwaysKept(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())
	tb.Add(span("trace1", "s1", "", "GET /health", "OK"))
	tb.Add(span("trace1", "s2", "s1", "db.query", "ERROR"))

	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		if len(spans) != 2 {
			t.Fatalf("want 2 spans for error trace, got %d", len(spans))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no spans emitted for error trace")
	}
}

func TestTraceBuffer_NonErrorDroppedByRatio(t *testing.T) {
	// Ratio 1000000 so almost nothing passes; deterministically check a known trace.
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())

	// Use a trace ID that we know does NOT sample at ratio 1000000.
	// "aaaaaaaaaaaaaaaa..." → upper bytes all 0xaa → 0xaaaa...aa % 1000000 ≠ 0
	noSampleID := "aaaaaaaaaaaaaaaa" + "aaaaaaaaaaaaaaaa"
	tb.Add(span(noSampleID, "s1", "", "GET /health", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		t.Fatalf("expected trace to be dropped, got %d spans", len(spans))
	case <-time.After(50 * time.Millisecond):
		// correct — nothing emitted
	}
}

func TestTraceBuffer_RatioOneKeepsAll(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1), testLogger())
	tb.Add(span("trace1", "s1", "", "POST /api/data", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	select {
	case <-tb.Out:
		// correct
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ratio=1 should keep everything")
	}
}

func TestTraceBuffer_RootSpanDeterminesOperation(t *testing.T) {
	// The root span (no parentSpanID) determines the ratio lookup key for the
	// keep decision. The buffer also consults the lookup at admission, to order
	// eviction, and that can happen before the root span has arrived — so assert
	// on the decision that actually decides storage: the last call, from keep().
	lookup := &opRecorder{}
	tb := NewTraceBuffer(10*time.Millisecond, lookup, testLogger())
	tb.Add(span("t1", "child", "root", "child-op", "OK")) // child first
	tb.Add(span("t1", "root", "", "root-op", "OK"))       // root second
	tb.Flush(time.Now().Add(time.Second))

	if len(lookup.seen) == 0 {
		t.Fatal("lookup not called")
	}
	if last := lookup.seen[len(lookup.seen)-1]; last != "root-op" {
		t.Errorf("keep decision used operation %q, want root-op", last)
	}
	for _, op := range lookup.seen {
		if op != "" && op != "root-op" {
			t.Errorf("lookup called with %q — only the root operation may key the ratio", op)
		}
	}
}

// opRecorder records every operation the buffer looks a ratio up for and keeps
// everything (ratio=1).
type opRecorder struct{ seen []string }

func (l *opRecorder) Ratio(_ context.Context, _ int64, op string) int {
	l.seen = append(l.seen, op)
	return 1
}

// TestTraceBuffer_CapBoundsSpanCount pins the memory bound. The cap is what
// keeps the reader (448MiB GOMEMLIMIT) alive under an ingest burst: buffered
// spans are live data the GC cannot reclaim for the whole TTL. Eviction changed
// *which* spans are lost, not whether the bound holds.
func TestTraceBuffer_CapBoundsSpanCount(t *testing.T) {
	tb := NewTraceBufferWithLimits(time.Hour, 2, NewStaticRatioLookup(1), testLogger())

	for i := 0; i < 50; i++ {
		tb.Add(span(fmt.Sprintf("%032x", i), "s1", "", "op", "OK"))
	}

	st := tb.Stats()
	if st.BufferedSpans > 2 {
		t.Fatalf("buffered spans = %d, want <= 2 — the cap is not bounding memory", st.BufferedSpans)
	}
	if st.BufferedTraces > 2 {
		t.Fatalf("buffered traces = %d, want <= 2", st.BufferedTraces)
	}
}

// TestTraceBuffer_CapKeepsErrorTraces guards the contract that made removing the
// self-instrument sampling bypass safe: error traces must survive even when the
// buffer is over capacity.
func TestTraceBuffer_CapKeepsErrorTraces(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1000000), testLogger())
	tb.maxSpans = 1

	// First span creates the trace and marks it an error.
	tb.Add(span("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s1", "", "op", "ERROR"))
	// Now at cap: a further span on the same error trace is still accepted...
	tb.Add(span("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s2", "s1", "db.query", "OK"))
	// ...but a brand-new non-error trace is not.
	tb.Add(span("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "s1", "", "op", "OK"))

	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		if len(spans) != 2 {
			t.Fatalf("want 2 spans on the error trace, got %d", len(spans))
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("error trace was dropped at cap — the always-pass contract is broken")
	}
}

// TestTraceBuffer_FlushReleasesCapBudget ensures the cap is not a one-way latch:
// flushed traces must return their span budget, or the buffer wedges permanently
// after the first burst.
func TestTraceBuffer_FlushReleasesCapBudget(t *testing.T) {
	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1), testLogger())
	tb.maxSpans = 1

	tb.Add(span("t1", "s1", "", "op", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	tb.mu.Lock()
	count := tb.spanCount
	tb.mu.Unlock()
	if count != 0 {
		t.Fatalf("spanCount after flush = %d, want 0", count)
	}

	// The buffer must accept work again.
	tb.Add(span("t2", "s1", "", "op", "OK"))
	tb.mu.Lock()
	held, dropped := len(tb.traces), tb.refused
	tb.mu.Unlock()
	if held != 1 || dropped != 0 {
		t.Fatalf("after flush: traces=%d dropped=%d, want 1 and 0", held, dropped)
	}
}

// TestTraceBuffer_OutBlockedIsReportedNotSilent pins that a stalled drain cannot
// silently swallow traces that survived sampling. The buffer's one guarantee is
// that error traces always pass; a bare `default:` drop broke that invisibly.
func TestTraceBuffer_OutBlockedIsReportedNotSilent(t *testing.T) {
	warns := 0
	logger := slog.New(warnCollector{warns: &warns})

	tb := NewTraceBuffer(10*time.Millisecond, NewStaticRatioLookup(1), logger)
	// Fill the out channel so delivery cannot succeed.
	for i := 0; i < cap(tb.out); i++ {
		tb.out <- nil
	}

	tb.Add(span("t1", "s1", "", "op", "ERROR"))

	done := make(chan struct{})
	go func() { tb.Flush(time.Now().Add(time.Second)); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Flush wedged behind a stalled drain — the gc loop must stay bounded")
	}

	if warns == 0 {
		t.Fatal("a kept trace was dropped with no warning — this is the silent loss we are fixing")
	}
}

// warnCollector counts WARN records.
type warnCollector struct{ warns *int }

func (h warnCollector) Enabled(context.Context, slog.Level) bool { return true }
func (h warnCollector) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.warns++
	}
	return nil
}
func (h warnCollector) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h warnCollector) WithGroup(string) slog.Handler      { return h }

// projectRatios is a SampleRatioLookup keyed by project, so a test can mix
// throwaway traffic with traffic that must be kept.
type projectRatios map[int64]int

func (p projectRatios) Ratio(_ context.Context, projectID int64, _ string) int {
	if r, ok := p[projectID]; ok {
		return r
	}
	return DefaultSampleRatio
}

func spanFor(projectID int64, traceID, spanID, parentID, name, status string) model.SpanRecord {
	rec := span(traceID, spanID, parentID, name, status)
	rec.ProjectID = projectID
	return rec
}

// sacrificialIDs returns n trace IDs that ratio sampling will discard, i.e.
// traces the buffer is holding for nothing and should sacrifice first.
func sacrificialIDs(ratio, n int) []string {
	out := make([]string, 0, n)
	for i := 0; len(out) < n; i++ {
		id := fmt.Sprintf("%016x%016x", uint64(i)+0x5eed, uint64(i))
		if !traceIDSampledByRatio(id, ratio) {
			out = append(out, id)
		}
	}
	return out
}

// drain collects everything the buffer emitted, keyed by trace ID.
func drain(tb *TraceBuffer) map[string][]model.SpanRecord {
	got := map[string][]model.SpanRecord{}
	for {
		select {
		case spans := <-tb.Out:
			if len(spans) > 0 {
				got[spans[0].TraceID] = spans
			}
		default:
			return got
		}
	}
}

// TestTraceBuffer_OneShotTraceSurvivesAtCap is the regression test for #166.
//
// At cap the buffer used to refuse trace IDs it had not seen before. That reads
// as even-handed and is not: a continuously-emitting service loses a slice of
// its spans and keeps the trace, while a one-shot producer — a CronJob, a CLI,
// a serverless invocation — emits its whole trace in a single batch and loses
// the entire run. Five daily CronJobs were unobservable for a month that way.
//
// The property: a trace emitted in one batch must not be less likely to survive
// than one dribbled out over minutes.
func TestTraceBuffer_OneShotTraceSurvivesAtCap(t *testing.T) {
	const chattyRatio = 1_000_000
	// Project 1 is sampled into oblivion; project 2 keeps everything.
	tb := NewTraceBufferWithLimits(time.Hour, 4, projectRatios{1: chattyRatio, 2: 1}, testLogger())

	// A chatty service has pinned the buffer at its cap.
	for _, id := range sacrificialIDs(chattyRatio, 4) {
		tb.Add(spanFor(1, id, "s1", "", "GET /health", "OK"))
	}
	if got := tb.Stats().BufferedSpans; got != 4 {
		t.Fatalf("setup: buffered spans = %d, want the buffer pinned at its cap of 4", got)
	}

	// A CronJob run now arrives as a single batch, all at once, at cap.
	const cronTrace = "c0ffee0000000001c0ffee0000000001"
	tb.Add(spanFor(2, cronTrace, "root", "", "cron.run", "OK"))
	tb.Add(spanFor(2, cronTrace, "s2", "root", "cron.step", "OK"))
	tb.Add(spanFor(2, cronTrace, "s3", "root", "cron.finish", "OK"))

	tb.Flush(time.Now().Add(2 * time.Hour))

	got := drain(tb)
	spans, ok := got[cronTrace]
	if !ok {
		t.Fatal("the one-shot trace was lost at cap — a single-batch producer gets exactly one chance")
	}
	if len(spans) != 3 {
		t.Fatalf("one-shot trace kept %d/3 spans; a whole run must survive intact", len(spans))
	}
}

// TestTraceBuffer_NewErrorTraceAdmittedAtCap pins the guarantee the buffer
// documents. Refusing unseen trace IDs at cap silently voided it: the error
// latch sits below the admission check, so a trace whose first span is an error
// never got created for the latch to fire on. Error traces are the reason for
// tail sampling at all — they must pass at cap too.
func TestTraceBuffer_NewErrorTraceAdmittedAtCap(t *testing.T) {
	const chattyRatio = 1_000_000
	tb := NewTraceBufferWithLimits(time.Hour, 3, projectRatios{1: chattyRatio}, testLogger())

	for _, id := range sacrificialIDs(chattyRatio, 3) {
		tb.Add(spanFor(1, id, "s1", "", "GET /health", "OK"))
	}

	const errTrace = "dead0000000000beefdead0000000001"
	tb.Add(spanFor(1, errTrace, "root", "", "cron.run", "ERROR"))

	tb.Flush(time.Now().Add(2 * time.Hour))

	if _, ok := drain(tb)[errTrace]; !ok {
		t.Fatal("an error trace first seen at cap was dropped — the always-pass contract is broken")
	}
}

// TestTraceBuffer_EvictsSacrificialBeforeKeepable pins the eviction order.
// Traces sampling is going to discard at flush are occupying the cap for
// nothing; sacrificing one costs no stored telemetry, so they go first and a
// keepable trace is only touched once none are left.
func TestTraceBuffer_EvictsSacrificialBeforeKeepable(t *testing.T) {
	const chattyRatio = 1_000_000
	tb := NewTraceBufferWithLimits(time.Hour, 3, projectRatios{1: chattyRatio, 2: 1}, testLogger())

	const keepTrace = "0badc0de0badc0de0badc0de0badc0de"
	tb.Add(spanFor(2, keepTrace, "root", "", "checkout", "OK")) // oldest, but keepable
	sac := sacrificialIDs(chattyRatio, 2)
	for _, id := range sac {
		tb.Add(spanFor(1, id, "s1", "", "GET /health", "OK"))
	}

	// At cap. The next arrival must free space from the throwaway traffic.
	const newTrace = "0badc0de0badc0de0badc0de0badc0df"
	tb.Add(spanFor(2, newTrace, "root", "", "checkout", "OK"))

	tb.mu.Lock()
	_, keepHeld := tb.traces[keepTrace]
	stillSacrificial := 0
	for _, id := range sac {
		if _, ok := tb.traces[id]; ok {
			stillSacrificial++
		}
	}
	tb.mu.Unlock()

	if !keepHeld {
		t.Fatal("evicted a keepable trace while throwaway traces were still buffered")
	}
	if stillSacrificial != 1 {
		t.Fatalf("sacrificial traces still buffered = %d, want 1 (exactly one freed)", stillSacrificial)
	}
	if lost := tb.Stats().EvictedKeptSpans + tb.Stats().RefusedSpans; lost != 0 {
		t.Fatalf("costly loss = %d spans, want 0 — shedding unsampled traffic must be free", lost)
	}
}

// TestTraceBuffer_EvictsOldestKeepableWhenNothingSacrificial covers the
// fallback: when every buffered trace would be kept, the cap still has to hold.
// Eviction is oldest-first, which gives every trace the same hazard regardless
// of whether its spans arrived in one batch or over minutes.
func TestTraceBuffer_EvictsOldestKeepableWhenNothingSacrificial(t *testing.T) {
	tb := NewTraceBufferWithLimits(time.Hour, 2, NewStaticRatioLookup(1), testLogger())

	tb.Add(span("00000000000000000000000000000001", "s1", "", "op", "OK"))
	tb.Add(span("00000000000000000000000000000002", "s1", "", "op", "OK"))
	tb.Add(span("00000000000000000000000000000003", "s1", "", "op", "OK"))

	tb.mu.Lock()
	_, oldestHeld := tb.traces["00000000000000000000000000000001"]
	_, newestHeld := tb.traces["00000000000000000000000000000003"]
	held := len(tb.traces)
	tb.mu.Unlock()

	if oldestHeld {
		t.Error("oldest keepable trace should have been evicted to admit the newcomer")
	}
	if !newestHeld {
		t.Error("newcomer refused — this is the admission refusal #166 is about")
	}
	if held != 2 {
		t.Errorf("buffered traces = %d, want 2", held)
	}
	if tb.Stats().EvictedKeptSpans == 0 {
		t.Error("evicting a keepable trace must be counted as real loss, not shrugged off")
	}
}

// TestTraceBuffer_ErrorTracesAreNeverEvicted keeps the always-pass contract
// intact under the new eviction path: error traces are not eligible victims, so
// a buffer full of them refuses rather than cannibalising itself.
func TestTraceBuffer_ErrorTracesAreNeverEvicted(t *testing.T) {
	tb := NewTraceBufferWithLimits(time.Hour, 2, NewStaticRatioLookup(1), testLogger())

	tb.Add(span("00000000000000000000000000000001", "s1", "", "op", "ERROR"))
	tb.Add(span("00000000000000000000000000000002", "s1", "", "op", "ERROR"))
	// Nothing evictable remains, so this non-error span is genuinely refused.
	tb.Add(span("00000000000000000000000000000003", "s1", "", "op", "OK"))

	tb.mu.Lock()
	held := len(tb.traces)
	tb.mu.Unlock()
	if held != 2 {
		t.Fatalf("buffered traces = %d, want the 2 error traces untouched", held)
	}
	if got := tb.Stats().RefusedSpans; got != 1 {
		t.Fatalf("refused spans = %d, want 1", got)
	}
	if got := tb.Stats().EvictedKeptSpans; got != 0 {
		t.Fatalf("evicted %d spans from error traces — they must never be victims", got)
	}
}

// TestTraceBuffer_EvictionQueuesDoNotGrowUnbounded guards the bookkeeping the
// eviction order needs. The queues are append-only on the hot path; if Flush did
// not prune them, a buffer under no cap pressure would leak one string per trace
// forever — a memory bug introduced by the fix for a memory bug.
func TestTraceBuffer_EvictionQueuesDoNotGrowUnbounded(t *testing.T) {
	tb := NewTraceBufferWithLimits(time.Millisecond, 0, NewStaticRatioLookup(1000), testLogger())

	for round := 0; round < 5; round++ {
		for i := 0; i < 200; i++ {
			tb.Add(span(fmt.Sprintf("%016x%016x", round, i), "s1", "", "op", "OK"))
		}
		tb.Flush(time.Now().Add(time.Hour))
	}

	tb.mu.Lock()
	anyLen, sacLen, traces := len(tb.anyQ), len(tb.sacrificeQ), len(tb.traces)
	tb.mu.Unlock()

	if traces != 0 {
		t.Fatalf("buffered traces = %d, want 0 after flushing everything", traces)
	}
	if anyLen != 0 || sacLen != 0 {
		t.Fatalf("eviction queues retained %d/%d entries for traces that are gone", anyLen, sacLen)
	}
}

// TestTraceBuffer_StatsReportCapPressure pins that cap pressure is a number and
// not just a log line. A month of missing CronJob traces had to be excavated
// precisely because the drop counter never left the log stream.
func TestTraceBuffer_StatsReportCapPressure(t *testing.T) {
	const chattyRatio = 1_000_000
	tb := NewTraceBufferWithLimits(time.Hour, 2, projectRatios{1: chattyRatio, 2: 1}, testLogger())

	for _, id := range sacrificialIDs(chattyRatio, 2) {
		tb.Add(spanFor(1, id, "s1", "", "GET /health", "OK"))
	}
	tb.Add(spanFor(2, "c0ffee0000000001c0ffee0000000002", "root", "", "cron.run", "OK"))

	st := tb.Stats()
	if st.MaxSpans != 2 {
		t.Errorf("MaxSpans = %d, want 2", st.MaxSpans)
	}
	if st.BufferedSpans != 2 {
		t.Errorf("BufferedSpans = %d, want 2", st.BufferedSpans)
	}
	if st.EvictedSacrificialSpans != 1 {
		t.Errorf("EvictedSacrificialSpans = %d, want 1", st.EvictedSacrificialSpans)
	}
	if st.EvictedKeptSpans != 0 || st.RefusedSpans != 0 {
		t.Errorf("costly loss = %d evicted / %d refused, want 0/0", st.EvictedKeptSpans, st.RefusedSpans)
	}
}

// TestTraceBuffer_SheddingUnsampledTrafficIsNotAWarning keeps the alert stream
// honest. BugBarn files an issue for anything at WARN or above, so a buffer
// shedding traces that sampling was going to discard anyway must not page
// anyone — while loss that actually costs stored telemetry still must.
func TestTraceBuffer_SheddingUnsampledTrafficIsNotAWarning(t *testing.T) {
	const chattyRatio = 1_000_000
	warns := 0
	tb := NewTraceBufferWithLimits(time.Hour, 2, projectRatios{1: chattyRatio},
		slog.New(warnCollector{warns: &warns}))

	for _, id := range sacrificialIDs(chattyRatio, 3) {
		tb.Add(spanFor(1, id, "s1", "", "GET /health", "OK"))
	}
	tb.Flush(time.Now())
	if warns != 0 {
		t.Fatalf("shedding unsampled traffic logged %d warnings — this fires every 30s and files issues", warns)
	}

	// Real loss must still be loud.
	tb.Add(span("00000000000000000000000000000001", "s1", "", "op", "ERROR"))
	tb.Add(span("00000000000000000000000000000002", "s1", "", "op", "ERROR"))
	tb.Add(span("00000000000000000000000000000003", "s1", "", "op", "OK"))
	tb.Flush(time.Now())
	if warns == 0 {
		t.Fatal("refusing spans outright must warn — that is unavoidable telemetry loss")
	}
}
