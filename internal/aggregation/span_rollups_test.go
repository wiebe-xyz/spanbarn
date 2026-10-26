package aggregation

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

type fakeSpanRollupWriter struct {
	mu   sync.Mutex
	rows []repository.SpanRollup
	err  error
}

func (f *fakeSpanRollupWriter) AddSpanRollups(rows []repository.SpanRollup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func newTestRollups(w SpanRollupWriter, cfg SpanRollupConfig, now time.Time) *SpanRollups {
	r := NewSpanRollups(w, cfg, nil)
	r.now = func() time.Time { return now }
	return r
}

func byName(rows []repository.SpanRollup) map[string]repository.SpanRollup {
	out := map[string]repository.SpanRollup{}
	for _, r := range rows {
		out[fmt.Sprintf("%s/%s/%s/%v/%s/%d", r.Service, r.Name, r.Kind, r.IsRoot, r.Status, r.Bucket.Unix())] = r
	}
	return out
}

func TestSpanRollupsFoldByArrivalBucketAndKey(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	at := time.Date(2026, 10, 26, 12, 7, 30, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, at)
	bucket := time.Date(2026, 10, 26, 12, 5, 0, 0, time.UTC)

	for _, s := range []repository.Span{
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "SERVER", Status: "ok", DurationUs: 100, IngestedAt: at, SampleWeight: 10},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Status: "OK", DurationUs: 300, IngestedAt: at.Add(time.Minute)},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Status: "error", DurationUs: 50, IngestedAt: at},
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", ParentSpanID: "p", DurationUs: 7, IngestedAt: at},
		// No arrival time: the clock's bucket.
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Status: "ok", DurationUs: 200},
		// Next bucket.
		{ProjectID: 1, Service: "api", Name: "GET /", Kind: "server", Status: "ok", DurationUs: 1, IngestedAt: bucket.Add(5 * time.Minute)},
	} {
		r.Add(s)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	got := byName(w.rows)
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(w.rows), w.rows)
	}
	ok := got[fmt.Sprintf("api/GET //server/true/ok/%d", bucket.Unix())]
	if ok.Count != 3 || ok.SumDurationUs != 600 || ok.MinUs != 100 || ok.MaxUs != 300 ||
		ok.WeightedCount != 10 || ok.WeightedSpans != 1 || ok.StepSeconds != SpanRollupStep {
		t.Errorf("ok root row = %+v", ok)
	}
	h, err := spanhist.Decode(ok.Hist)
	if err != nil || h.Count() != 3 || h.Percentile(50) != 200 {
		t.Errorf("hist count=%d p50=%d err=%v", h.Count(), h.Percentile(50), err)
	}
	if child := got[fmt.Sprintf("api/GET //server/false/unset/%d", bucket.Unix())]; child.Count != 1 {
		t.Errorf("child row missing or wrong: %+v", child)
	}
	if next := got[fmt.Sprintf("api/GET //server/true/ok/%d", bucket.Add(5*time.Minute).Unix())]; next.Count != 1 {
		t.Errorf("next bucket row = %+v", next)
	}

	// A second flush with nothing new writes nothing.
	w.rows = nil
	if err := r.Flush(); err != nil || len(w.rows) != 0 {
		t.Errorf("empty flush wrote %d rows, err %v", len(w.rows), err)
	}
}

func TestSpanRollupsCapNamesAndServicesPerHour(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{MaxNames: 2, MaxServices: 2}, at)
	add := func(service, name string, when time.Time) {
		r.Add(repository.Span{ProjectID: 1, Service: service, Name: name, Kind: "server", Status: "ok", DurationUs: 1, IngestedAt: when})
	}
	add("api", "a", at)
	add("api", "b", at)
	add("api", "c", at.Add(10*time.Minute)) // third name in the hour
	add("api", "a", at.Add(20*time.Minute)) // admitted earlier
	add("web", "x", at)
	add("jobs", "y", at)               // third service
	add("api", "c", at.Add(time.Hour)) // a new hour admits it

	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, row := range w.rows {
		names[row.Service+"/"+row.Name+"@"+row.Bucket.Format("15:04")] += row.Count
	}
	want := map[string]int64{
		"api/a@12:00": 1, "api/b@12:00": 1, "api/~other@12:10": 1, "api/a@12:20": 1,
		"web/x@12:00": 1, "~other/~other@12:00": 1, "api/c@13:00": 1,
	}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("rows = %v, want %v", names, want)
	}
}

func TestSpanRollupsRequeueOnFailedFlush(t *testing.T) {
	w := &fakeSpanRollupWriter{err: errors.New("disk full")}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := newTestRollups(w, SpanRollupConfig{}, now)
	span := repository.Span{ProjectID: 1, Service: "api", Name: "op", Kind: "server", Status: "ok", DurationUs: 10, IngestedAt: now}
	stale := span
	stale.IngestedAt = now.Add(-2 * time.Hour)
	r.Add(span)
	r.Add(stale)
	if err := r.Flush(); err == nil {
		t.Fatal("flush with a failing writer returned nil")
	}
	r.Add(span) // arrives while the first flush is being put back

	w.err = nil
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.rows) != 1 || w.rows[0].Count != 2 || w.rows[0].SumDurationUs != 20 {
		t.Errorf("after retry rows = %+v, want one row of 2 spans (the stale bucket dropped)", w.rows)
	}
	if h, _ := spanhist.Decode(w.rows[0].Hist); h.Count() != 2 {
		t.Errorf("requeued hist count = %d, want 2", h.Count())
	}
}

func TestAccumulatorPassesSpansToRollups(t *testing.T) {
	w := &fakeSpanRollupWriter{}
	r := NewSpanRollups(w, SpanRollupConfig{}, nil)
	acc := NewAccumulator(&mockAccWriter{}, time.Minute, time.Hour, nil)
	acc.SetSpanRollups(r)
	acc.Add(repository.Span{ProjectID: 1, Service: "api", Name: "op", Kind: "server", DurationUs: 5, StartTimeUs: time.Now().UnixMicro()})
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(w.rows) != 1 || w.rows[0].Count != 1 {
		t.Errorf("rollup rows = %+v", w.rows)
	}
}

func TestSpanRollupCapsPruneOldHours(t *testing.T) {
	c := newRollupCaps()
	cfg := SpanRollupConfig{}.withDefaults()
	base := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC).Unix()
	c.admit(1, "api", "op", base, cfg)
	c.admit(1, "api", "op", base+3*3600, cfg)
	c.overflowed[capKey{hour: base, projectID: 1}] = struct{}{}
	c.prune(base + 3*3600 + 60)
	for _, m := range []map[capKey]map[string]struct{}{c.services, c.names} {
		for k := range m {
			if k.hour == base {
				t.Errorf("hour %d kept after prune", k.hour)
			}
		}
	}
	if len(c.overflowed) != 0 {
		t.Errorf("overflow marks kept: %v", c.overflowed)
	}
}
