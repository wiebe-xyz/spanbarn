package aggregation

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

func edgesByKey(rows []repository.SpanEdge) map[string]repository.SpanEdge {
	out := map[string]repository.SpanEdge{}
	for _, e := range rows {
		out[fmt.Sprintf("%d/%s/%s/%s/%d", e.ProjectID, e.Source, e.Target, e.TargetType, e.Bucket.Unix())] = e
	}
	return out
}

func TestSpanEdgesFromABatch(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	at := time.Date(2026, 10, 26, 12, 7, 30, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, at)
	bucket := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC).Unix()
	next := bucket + 300

	r.AddBatch([]repository.Span{
		{ProjectID: 1, TraceID: "t", SpanID: "a", Service: "web", Kind: "server", DurationUs: 900, Status: "error", IngestedAt: at},
		{ProjectID: 1, TraceID: "t", SpanID: "b", ParentSpanID: "a", Service: "web", Kind: "client",
			Attributes: `{"db.system":"sqlite"}`, DurationUs: 40, SampleWeight: 5, IngestedAt: at},
		{ProjectID: 1, TraceID: "t", SpanID: "c", ParentSpanID: "b", Service: "db", Kind: "server", DurationUs: 30, IngestedAt: at},
		// Arrived in the next bucket: its edge and a marker land there.
		{ProjectID: 1, TraceID: "u", SpanID: "d", Service: "web", Kind: "CLIENT",
			Attributes: `{"db.system":"sqlite"}`, DurationUs: 60, Status: "ERROR", IngestedAt: at.Add(5 * time.Minute)},
	})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	got := edgesByKey(w.edges)
	if len(w.edges) != 5 {
		t.Fatalf("edges = %+v, want two markers and three edges", w.edges)
	}
	for _, b := range []int64{bucket, next} {
		if m, ok := got[fmt.Sprintf("0////%d", b)]; !ok || m.Count != 0 {
			t.Errorf("marker of %d = %+v, %v", b, m, ok)
		}
	}
	db := got[fmt.Sprintf("1/web/sqlite/database/%d", bucket)]
	if db.Count != 1 || db.ErrorCount != 0 || db.WeightedCount != 5 || db.WeightedSpans != 1 || db.SumDurationUs != 40 {
		t.Errorf("client edge = %+v", db)
	}
	// The call into db is carried by its parent, the client span.
	svc := got[fmt.Sprintf("1/web/db/service/%d", bucket)]
	if svc.Count != 1 || svc.MinUs != 40 {
		t.Errorf("cross-service edge = %+v", svc)
	}
	late := got[fmt.Sprintf("1/web/sqlite/database/%d", next)]
	if late.Count != 1 || late.ErrorCount != 1 {
		t.Errorf("next bucket's edge = %+v", late)
	}
	if h, _ := spanhist.Decode(db.Hist); h.Count() != 1 {
		t.Errorf("hist count = %d", h.Count())
	}
}

func TestSpanEdgesCapTargetsPerSource(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	at := time.Date(2026, 10, 26, 12, 7, 30, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{MaxEdgeTargets: 2}, at)
	var batch []repository.Span
	for i := range 4 {
		batch = append(batch, repository.Span{ProjectID: 1, TraceID: "t", SpanID: fmt.Sprint(i), Service: "web", Kind: "client",
			Attributes: fmt.Sprintf(`{"http.url":"https://h%d.example.com/"}`, i), DurationUs: 10, IngestedAt: at})
	}
	r.AddBatch(batch)
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	targets := map[string]int64{}
	for _, e := range w.edges {
		if !e.IsMarker() {
			targets[e.Target] += e.Count
		}
	}
	if len(targets) != 3 || targets[OtherName] != 2 {
		t.Errorf("targets = %v, want two hosts and the rest under %s", targets, OtherName)
	}
}

// A failed edge write keeps the edges and markers for the next flush, while
// the rollups it was flushed with are written once.
func TestSpanEdgesRequeueOnFailure(t *testing.T) {
	w := &fakeSpanRollupWriter{edgeErr: errors.New("busy")}
	at := time.Date(2026, 10, 26, 12, 7, 30, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, at)
	span := repository.Span{ProjectID: 1, TraceID: "t", SpanID: "a", Service: "web", Kind: "client",
		Attributes: `{"db.system":"sqlite"}`, DurationUs: 10, IngestedAt: at}
	r.AddBatch([]repository.Span{span})
	if err := r.Flush(); err == nil {
		t.Fatal("flush hid the edge write error")
	}
	span.SpanID, span.DurationUs = "b", 30
	r.AddBatch([]repository.Span{span})
	w.edgeErr = nil
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.rows) != 2 {
		t.Errorf("rollup rows = %d, want one per flush", len(w.rows))
	}
	var edge repository.SpanEdge
	markers := 0
	for _, e := range w.edges {
		if e.IsMarker() {
			markers++
		} else {
			edge = e
		}
	}
	if markers != 1 || edge.Count != 2 || edge.MinUs != 10 || edge.MaxUs != 30 {
		t.Errorf("edges after retry = %+v", w.edges)
	}
}

func TestAccumulatorPassesBatchesToEdges(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	r := NewSpanRollups(w, SpanRollupConfig{}, nil)
	acc := NewAccumulator(&mockAccWriter{}, time.Minute, time.Hour, nil)
	acc.SetSpanRollups(r)
	acc.AddBatch([]repository.Span{{ProjectID: 1, Service: "api", Name: "q", Kind: "client",
		Attributes: `{"db.system":"sqlite"}`, DurationUs: 5, StartTimeUs: time.Now().UnixMicro()}})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.rows) != 1 || len(w.edges) != 2 {
		t.Errorf("rollups %+v, edges %+v", w.rows, w.edges)
	}
	if got := acc.QueryRecent(repository.AggregateFilter{}); len(got) != 1 {
		t.Errorf("accumulator slots = %d, want 1", len(got))
	}
}
