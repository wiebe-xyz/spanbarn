package ingest

import (
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

const weightRatio = 4

// weightTraceID returns a trace ID that the ratio sample at weightRatio keeps
// (sampled) or drops. Found by search, so the test does not depend on the
// hash's values.
func weightTraceID(t *testing.T, sampled bool, skip int) string {
	t.Helper()
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("%016x%016x", i, i)
		if ratioSampled(id, weightRatio) == sampled {
			if skip == 0 {
				return id
			}
			skip--
		}
	}
	t.Fatal("no trace id found")
	return ""
}

func weightBuffer(rules RescueRules) *TraceBuffer {
	return NewTraceBufferWithRules(10*time.Millisecond, 0, rules, NewStaticRatioLookup(weightRatio), testLogger())
}

func weightTrace(id string, root model.SpanRecord, extra ...model.SpanRecord) *bufferedTrace {
	tr := &bufferedTrace{id: id, rootOp: root.Name, projectID: root.ProjectID, spans: append([]model.SpanRecord{root}, extra...)}
	for _, s := range tr.spans {
		if s.Status == "error" {
			tr.hasError = true
		}
	}
	return tr
}

// TestKeepWeight covers every way a trace can be kept, alone and combined
// with the ratio sample. A trace kept for a reason that keeps every trace of
// its kind stands for itself, even when the ratio sample also picked it.
func TestKeepWeight(t *testing.T) {
	now := time.Now()
	sampled := weightTraceID(t, true, 0)
	dropped := weightTraceID(t, false, 0)
	dropped2 := weightTraceID(t, false, 1)

	cases := []struct {
		name  string
		rules RescueRules
		tr    *bufferedTrace
		want  int
	}{
		{"sampled only", RescueRules{}, weightTrace(sampled, rootSpan(sampled, "GET /", 1000)), weightRatio},
		{"error and sampled", RescueRules{}, weightTrace(sampled, rootSpan(sampled, "GET /", 1000), span(sampled, "s2", "s1", "db", "error")), 1},
		{"error only", RescueRules{}, weightTrace(dropped, rootSpan(dropped, "GET /", 1000), span(dropped, "s2", "s1", "db", "error")), 1},
		{"slow and sampled", RescueRules{SlowThresholdUs: 500}, weightTrace(sampled, rootSpan(sampled, "GET /", 1000)), 1},
		{"slow only", RescueRules{SlowThresholdUs: 500}, weightTrace(dropped, rootSpan(dropped, "GET /", 1000)), 1},
		{"sampled and counted by the floor", RescueRules{MinTracesPerHour: 1}, weightTrace(sampled, rootSpan(sampled, "GET /", 1000)), weightRatio},
		{"rescued by the floor only", RescueRules{MinTracesPerHour: 1}, weightTrace(dropped, rootSpan(dropped, "GET /", 1000)), 1},
		{"dropped", RescueRules{}, weightTrace(dropped, rootSpan(dropped, "GET /", 1000)), 0},
		{"empty", RescueRules{}, &bufferedTrace{id: sampled}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tb := weightBuffer(c.rules)
			defer tb.Close(now)
			if got := tb.keepWeight(c.tr, now); got != c.want {
				t.Errorf("keepWeight = %d, want %d", got, c.want)
			}
		})
	}

	t.Run("floor spent", func(t *testing.T) {
		tb := weightBuffer(RescueRules{MinTracesPerHour: 1})
		defer tb.Close(now)
		tb.keepWeight(weightTrace(dropped, rootSpan(dropped, "GET /", 1000)), now)
		if got := tb.keepWeight(weightTrace(dropped2, rootSpan(dropped2, "GET /", 1000)), now); got != 0 {
			t.Errorf("second unsampled trace in the hour: keepWeight = %d, want 0", got)
		}
	})

	t.Run("ratio 1", func(t *testing.T) {
		tb := NewTraceBufferWithRules(10*time.Millisecond, 0, RescueRules{}, NewStaticRatioLookup(1), testLogger())
		defer tb.Close(now)
		if got := tb.keepWeight(weightTrace(dropped, rootSpan(dropped, "GET /", 1000)), now); got != 1 {
			t.Errorf("keepWeight = %d, want 1", got)
		}
	})
}

// TestFlushStampsWeightOnEverySpan checks that the weight reaches every span
// of a kept trace, children included, since aggregation counts spans.
func TestFlushStampsWeightOnEverySpan(t *testing.T) {
	tb := weightBuffer(RescueRules{})
	id := weightTraceID(t, true, 0)
	tb.Add(rootSpan(id, "GET /", 1000))
	tb.Add(span(id, "s2", "s1", "db.query", "OK"))
	tb.Flush(time.Now().Add(time.Second))

	select {
	case spans := <-tb.Out:
		if len(spans) != 2 {
			t.Fatalf("got %d spans, want 2", len(spans))
		}
		for _, s := range spans {
			if s.SampleWeight != weightRatio {
				t.Errorf("span %s: SampleWeight = %d, want %d", s.SpanID, s.SampleWeight, weightRatio)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("sampled trace was not flushed")
	}
}
