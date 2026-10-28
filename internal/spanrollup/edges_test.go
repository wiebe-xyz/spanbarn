package spanrollup

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// fiveMinuteEdge builds one 5-minute edge from api to sqlite of the given
// durations.
func fiveMinuteEdge(bucket time.Time, durations ...int64) repository.SpanEdge {
	var h spanhist.Hist
	e := repository.SpanEdge{
		StepSeconds: 300, Bucket: bucket, ProjectID: 1, Source: "api", Target: "sqlite", TargetType: "database",
		MinUs: durations[0], MaxUs: durations[0],
	}
	for _, d := range durations {
		h.Observe(d)
		e.Count++
		e.WeightedCount += 4
		e.WeightedSpans++
		e.SumDurationUs += d
		e.MinUs, e.MaxUs = min(e.MinUs, d), max(e.MaxUs, d)
	}
	e.Hist = h.Encode()
	return e
}

func edgeWindow(t *testing.T, repo *repository.Repository, step int64, from, to time.Time) []repository.SpanEdge {
	t.Helper()
	rows, err := repo.SpanEdgeWindow(context.Background(), step, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Two hours of 5-minute edges compact into one hourly edge and one hourly
// marker per hour. The second hour misses an edge bucket, so its edges stay
// out of the hourly tier while its span rollups are compacted.
func TestCompactorCompactsSpanEdges(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	h0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	var rollups []repository.SpanRollup
	var edges []repository.SpanEdge
	for i := range 24 {
		at := h0.Add(time.Duration(i) * 5 * time.Minute)
		rollups = append(rollups, fiveMinute(at, "GET /", 100))
		if i == 17 {
			continue
		}
		edges = append(edges, repository.SpanEdgeMarker(300, at), fiveMinuteEdge(at, int64(10+i), 900))
	}
	if err := repo.AddSpanRollups(rollups); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSpanEdges(edges); err != nil {
		t.Fatal(err)
	}
	now := h0.Add(2*time.Hour + 11*time.Minute)
	if err := newTestCompactor(repo, &now).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	first := edgeWindow(t, repo, 3600, h0, h0.Add(time.Hour))
	if len(first) != 2 || !first[0].IsMarker() {
		t.Fatalf("first hour = %+v, want a marker and one edge", first)
	}
	e := first[1]
	h, err := spanhist.Decode(e.Hist)
	if err != nil {
		t.Fatal(err)
	}
	if e.Count != 24 || e.WeightedCount != 96 || e.MinUs != 10 || e.MaxUs != 900 || h.Count() != 24 ||
		e.SumDurationUs != 12*900+(10+21)*12/2 {
		t.Errorf("hourly edge = %+v (hist %d)", e, h.Count())
	}
	if got := edgeWindow(t, repo, 3600, h0.Add(time.Hour), h0.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("edge hour with a gap = %+v, want no rows", got)
	}
	if got := window(t, repo, 3600, h0.Add(time.Hour), h0.Add(2*time.Hour)); len(got) != 1 || got[0].Count != 12 {
		t.Errorf("rollup hour = %+v, want one row of 12 spans", got)
	}
}

func TestMergeEdgesKeepsOneMarker(t *testing.T) {
	b := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rows := []repository.SpanEdge{
		repository.SpanEdgeMarker(300, b), fiveMinuteEdge(b, 10),
		repository.SpanEdgeMarker(300, b.Add(5*time.Minute)), fiveMinuteEdge(b.Add(5*time.Minute), 30),
	}
	got := MergeEdges(rows, 3600, b)
	if len(got) != 2 {
		t.Fatalf("merged = %+v, want a marker and one edge", got)
	}
	for _, e := range got {
		if e.StepSeconds != 3600 || !e.Bucket.Equal(b) {
			t.Errorf("row %+v not moved to the hourly bucket", e)
		}
		if !e.IsMarker() && (e.Count != 2 || e.MinUs != 10 || e.MaxUs != 30) {
			t.Errorf("edge = %+v", e)
		}
	}
}
