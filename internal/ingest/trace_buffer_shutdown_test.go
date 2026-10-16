package ingest

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// collect reads Out until Close closes it.
func collect(tb *TraceBuffer) <-chan []model.SpanRecord {
	done := make(chan []model.SpanRecord, 1)
	go func() {
		var all []model.SpanRecord
		for spans := range tb.Out {
			all = append(all, spans...)
		}
		done <- all
	}()
	return done
}

// A trace still inside its TTL at shutdown is decided then. Before Close
// existed the process exited with it still in the map, and every rollout lost
// the last TTL of traffic, error traces included.
func TestTraceBufferCloseDecidesTracesInsideTheirTTL(t *testing.T) {
	tb := NewTraceBuffer(time.Hour, NewStaticRatioLookup(1000000), testLogger())
	noSample := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tb.Add(span("errtrace", "s1", "", "POST /api/v1/devices/refresh", "OK"))
	tb.Add(span("errtrace", "s2", "s1", "db.query", "ERROR"))
	tb.Add(span(noSample, "s1", "", "GET /health", "OK"))
	out := collect(tb)

	res := tb.Close(time.Now())

	if res.Traces != 2 || res.Spans != 3 || res.Kept != 2 {
		t.Errorf("Close = %+v, want 2 traces, 3 spans, 2 kept", res)
	}
	select {
	case got := <-out:
		if len(got) != 2 || got[0].TraceID != "errtrace" {
			t.Errorf("Out carried %d spans, want the 2 of the error trace", len(got))
		}
	case <-time.After(time.Second):
		t.Fatal("Out not closed after Close")
	}
	if st := tb.Stats(); st.BufferedSpans != 0 || st.BufferedTraces != 0 {
		t.Errorf("buffer after Close = %+v, want empty", st)
	}
}

func TestTraceBufferCloseTwice(t *testing.T) {
	tb := NewTraceBuffer(time.Hour, NewStaticRatioLookup(1), testLogger())
	tb.Add(span("t1", "s1", "", "GET /", "OK"))
	out := collect(tb)

	if res := tb.Close(time.Now()); res.Kept != 1 {
		t.Errorf("first Close kept %d, want 1", res.Kept)
	}
	if res := tb.Close(time.Now()); res != (CloseResult{}) {
		t.Errorf("second Close = %+v, want zero", res)
	}
	if got := <-out; len(got) != 1 {
		t.Errorf("Out carried %d spans, want 1", len(got))
	}
}
