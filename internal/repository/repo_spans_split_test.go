package repository

import (
	"errors"
	"testing"
	"time"
)

func vitalSpan(trace, span, metric, rating, page string, durUs int64) Span {
	attrs := `{"webvital.rating":"` + rating + `"`
	if page != "" {
		attrs += `,"webvital.page":"` + page + `"`
	}
	attrs += `}`
	return Span{
		ProjectID: 1, TraceID: trace, SpanID: span, Name: "webvital." + metric,
		Service: "frontend", Kind: "client", Status: "ok", StartTimeUs: 1000,
		DurationUs: durUs, Attributes: attrs, Events: "[]",
	}
}

func TestQueryWebVitals(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertSpans([]Span{
		vitalSpan("v1", "a", "LCP", "good", "/home", 2000),
		vitalSpan("v2", "b", "CLS", "poor", "", 50),
		{ProjectID: 1, TraceID: "v3", SpanID: "c", Name: "GET /x", Service: "frontend", Kind: "server", Status: "ok", Attributes: "{}", Events: "[]"},
		{ProjectID: 1, TraceID: "v4", SpanID: "d", Name: "webvital.INP", Service: "other", Kind: "client", Status: "ok", Attributes: "not json", Events: "[]"},
	}); err != nil {
		t.Fatal(err)
	}

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)

	rows, err := repo.QueryWebVitals("frontend", from, to)
	if err != nil {
		t.Fatalf("QueryWebVitals: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (webvital spans of the service only)", len(rows))
	}
	byMetric := map[string]WebVitalRow{}
	for _, r := range rows {
		byMetric[r.Metric] = r
	}
	if r := byMetric["LCP"]; r.Page != "/home" || r.Rating != "good" || r.ValueUs != 2000 {
		t.Errorf("LCP row = %+v", r)
	}
	if r := byMetric["CLS"]; r.Page != "/" || r.Rating != "poor" {
		t.Errorf("CLS row must default the page to /, got %+v", r)
	}

	all, err := repo.QueryWebVitals("", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("unfiltered rows = %d, want 3", len(all))
	}
	if none, _ := repo.QueryWebVitals("frontend", to, time.Time{}); len(none) != 0 {
		t.Errorf("window after ingest must be empty, got %d", len(none))
	}
}

func TestQueryWebVitalsTimeseries(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertSpans([]Span{
		vitalSpan("v1", "a", "LCP", "good", "/home", 1000),
		vitalSpan("v2", "b", "LCP", "needs-improvement", "/home", 3000),
		vitalSpan("v3", "c", "LCP", "poor", "/home", 5000),
		vitalSpan("v4", "d", "LCP", "good", "/other", 9000),
		vitalSpan("v5", "e", "CLS", "good", "/home", 10),
	}); err != nil {
		t.Fatal(err)
	}

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)

	buckets, err := repo.QueryWebVitalsTimeseries("frontend", "/home", "LCP", from, to, 86400)
	if err != nil {
		t.Fatalf("QueryWebVitalsTimeseries: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("buckets = %+v, want one daily bucket", buckets)
	}
	b := buckets[0]
	if b.Samples != 3 || b.Good != 1 || b.NI != 1 || b.Poor != 1 {
		t.Errorf("counts = %+v", b)
	}
	if b.Page != "/home" || b.Metric != "LCP" || b.P50Us == 0 || b.P95Us < b.P50Us {
		t.Errorf("bucket = %+v", b)
	}

	allPages, err := repo.QueryWebVitalsTimeseries("", "", "LCP", from, to, 86400)
	if err != nil {
		t.Fatal(err)
	}
	if len(allPages) != 1 || allPages[0].Samples != 4 {
		t.Errorf("all pages = %+v", allPages)
	}
}

func TestStreamSpans(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertSpans([]Span{
		filterSpan("t1", "s1", "a", "web", "server", `{}`, 10),
		filterSpan("t1", "s2", "b", "web", "server", `{}`, 20),
		filterSpan("t2", "s3", "c", "db", "client", `{}`, 30),
	}); err != nil {
		t.Fatal(err)
	}

	var seen []string
	err := repo.StreamSpans(SpanFilter{ProjectID: 1, Service: "web"}, func(s Span) error {
		seen = append(seen, s.SpanID)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamSpans: %v", err)
	}
	if len(seen) != 2 {
		t.Errorf("streamed %v, want the 2 web spans", seen)
	}

	var limited int
	if err := repo.StreamSpans(SpanFilter{ProjectID: 1, Limit: 1}, func(Span) error { limited++; return nil }); err != nil || limited != 1 {
		t.Errorf("limit: err=%v streamed=%d", err, limited)
	}

	stop := errors.New("stop")
	if err := repo.StreamSpans(SpanFilter{ProjectID: 1}, func(Span) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("callback error must propagate, got %v", err)
	}
}

func TestSearchTraceSummariesFilters(t *testing.T) {
	repo := setupTestDB(t)
	root := func(trace, span, name, service, status string, dur int64) Span {
		return Span{
			ProjectID: 1, TraceID: trace, SpanID: span, Name: name, Service: service,
			Kind: "server", Status: status, StartTimeUs: 1000, DurationUs: dur,
			Attributes: "{}", Events: "[]",
		}
	}
	child := func(trace, span, parent string) Span {
		return Span{
			ProjectID: 1, TraceID: trace, SpanID: span, ParentSpanID: parent, Name: "child",
			Service: "web", Kind: "internal", Status: "ok", StartTimeUs: 1100, DurationUs: 5,
			Attributes: "{}", Events: "[]",
		}
	}
	if err := repo.InsertSpans([]Span{
		root("t1", "r1", "GET /a", "web", "ok", 100),
		child("t1", "c1", "r1"),
		root("t2", "r2", "GET /b", "api", "error", 9000),
		root("t3", "r3", "GET /health", "web", "ok", 10),
	}); err != nil {
		t.Fatal(err)
	}

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)

	ids := func(f SpanFilter, minSpans int) map[string]bool {
		t.Helper()
		f.ProjectID = 1
		rows, err := repo.SearchTraceSummaries(f, minSpans)
		if err != nil {
			t.Fatalf("SearchTraceSummaries(%+v): %v", f, err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.TraceID] = true
		}
		return out
	}

	tests := []struct {
		name     string
		filter   SpanFilter
		minSpans int
		want     []string
	}{
		{"no filter", SpanFilter{}, 0, []string{"t1", "t2", "t3"}},
		{"service", SpanFilter{Service: "api"}, 0, []string{"t2"}},
		{"operation", SpanFilter{Operation: "GET /a"}, 0, []string{"t1"}},
		{"status error", SpanFilter{Status: "error"}, 0, []string{"t2"}},
		{"status ok", SpanFilter{Status: "ok"}, 0, []string{"t1", "t3"}},
		{"min duration", SpanFilter{MinDuration: 1000}, 0, []string{"t2"}},
		{"time window", SpanFilter{From: from, To: to}, 0, []string{"t1", "t2", "t3"}},
		{"window in the future", SpanFilter{From: to}, 0, nil},
		{"exclusions", SpanFilter{ExcludeOperations: []string{"GET /health", "GET /b"}}, 0, []string{"t1"}},
		{"min spans", SpanFilter{}, 2, []string{"t1"}},
	}
	for _, tc := range tests {
		got := ids(tc.filter, tc.minSpans)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !got[w] {
				t.Errorf("%s: missing %s in %v", tc.name, w, got)
			}
		}
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: 1, SortErrorsFirst: true, Limit: 1}, 0)
	if err != nil || len(rows) != 1 || rows[0].TraceID != "t2" {
		t.Errorf("errors-first limit 1 = %+v, err=%v", rows, err)
	}
}
