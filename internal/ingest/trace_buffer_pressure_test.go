package ingest

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var (
	shedding = lossWindow{evictedSacrif: 10, maxSpans: 100, buffered: 99}
	nearCap  = lossWindow{maxSpans: 100, buffered: 90}
	belowCap = lossWindow{maxSpans: 100, buffered: 89}
)

// The buffer warns once it has stayed at its cap for the whole warn delay,
// counting windows near the cap with no eviction, and not again within the
// repeat interval.
func TestCapPressureWarnsAfterASustainedStretch(t *testing.T) {
	var p capPressure
	start := time.Unix(1_000_000, 0)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	if got := p.observe(shedding, at(0)); got != 0 {
		t.Fatalf("first window warned: %v", got)
	}
	if got := p.observe(nearCap, at(capPressureWarnAfter-time.Second)); got != 0 {
		t.Fatalf("warned before the delay: %v", got)
	}
	if got := p.observe(shedding, at(capPressureWarnAfter)); got != capPressureWarnAfter {
		t.Fatalf("at the delay got %v, want %v", got, capPressureWarnAfter)
	}
	if got := p.observe(shedding, at(capPressureWarnAfter+capPressureRepeat-time.Second)); got != 0 {
		t.Fatalf("warned again within the repeat interval: %v", got)
	}
	if got := p.observe(shedding, at(capPressureWarnAfter+capPressureRepeat)); got != capPressureWarnAfter+capPressureRepeat {
		t.Fatalf("after the repeat interval got %v", got)
	}
}

// A window below the cap with no eviction ends the stretch, so bursts never
// add up to a warning.
func TestCapPressureResetsBelowTheCap(t *testing.T) {
	var p capPressure
	start := time.Unix(1_000_000, 0)
	p.observe(shedding, start)
	p.observe(belowCap, start.Add(capPressureWarnAfter/2))
	if got := p.observe(shedding, start.Add(capPressureWarnAfter)); got != 0 {
		t.Fatalf("a broken stretch warned: %v", got)
	}
	if got := p.observe(shedding, start.Add(2*capPressureWarnAfter)); got != capPressureWarnAfter {
		t.Fatalf("new stretch got %v, want %v", got, capPressureWarnAfter)
	}
}

// A buffer held full through flushes logs the sustained-cap warning, at warn
// level so selflog reports it.
func TestFlushWarnsWhenTheBufferStaysFull(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tb := NewTraceBufferWithRules(time.Hour, 10, RescueRules{}, NewStaticRatioLookup(1), logger)
	clock := newSimClock()
	tb.now = clock.now
	defer tb.Close(clock.now())
	for i := 0; i < 10; i++ {
		tb.Add(childSpan(string(rune('a'+i)), "c", "OK"))
	}
	start := clock.now()
	tb.Flush(start)
	tb.Flush(start.Add(capPressureWarnAfter / 2))
	if strings.Contains(logs.String(), "sustained") {
		t.Fatalf("warned before the delay:\n%s", logs.String())
	}
	tb.Flush(start.Add(capPressureWarnAfter))
	if !strings.Contains(logs.String(), `"level":"WARN","msg":"trace buffer at capacity for a sustained stretch"`) {
		t.Fatalf("no sustained-cap warning:\n%s", logs.String())
	}
}
