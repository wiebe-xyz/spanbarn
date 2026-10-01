package repository

import (
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func analyzeSpan(i int, path, kind, status string, dur int64, extra string) Span {
	attrs := fmt.Sprintf(`{"url.path":%q,"user":"u%d"%s}`, path, i%3, extra)
	if path == "" {
		attrs = `{"user":"anon"}`
	}
	return Span{
		ProjectID: 1, TraceID: fmt.Sprintf("t%d", i), SpanID: fmt.Sprintf("s%d", i),
		Name: "op", Service: "web", Kind: kind, Status: status,
		StartTimeUs: 1000, DurationUs: dur, Attributes: attrs, Events: "[]",
	}
}

func seedAnalyze(t *testing.T) *Repository {
	t.Helper()
	repo := setupTestDB(t)
	var spans []Span
	// /a: ten server spans with durations 1..10 ms, two errors.
	for i := 1; i <= 10; i++ {
		st := "ok"
		if i > 8 {
			st = "error"
		}
		spans = append(spans, analyzeSpan(i, "/a", "server", st, int64(i)*1000, `,"http.response.status_code":200`))
	}
	// /b: four server spans, 100 us each.
	for i := 11; i <= 14; i++ {
		spans = append(spans, analyzeSpan(i, "/b", "server", "ok", 100, `,"http.response.status_code":500`))
	}
	// /c: two client spans, no path on one.
	spans = append(spans, analyzeSpan(15, "/c", "client", "ok", 50, ""))
	spans = append(spans, analyzeSpan(16, "", "client", "ok", 70, ""))
	// Another project, never counted.
	other := analyzeSpan(17, "/a", "server", "ok", 1, "")
	other.ProjectID = 2
	spans = append(spans, other)
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return repo
}

func baseQuery(calcs ...AnalyzeCalc) AnalyzeQuery {
	return AnalyzeQuery{
		ProjectID: 1,
		From:      time.Now().UTC().Add(-time.Hour),
		To:        time.Now().UTC().Add(time.Hour),
		Calcs:     calcs,
		Limit:     20,
		Desc:      true,
		MaxSpans:  100000,
	}
}

func rowFor(t *testing.T, rows []AnalyzeRow, group ...string) AnalyzeRow {
	t.Helper()
	for _, r := range rows {
		if len(r.Group) != len(group) {
			continue
		}
		match := true
		for i := range group {
			match = match && r.Group[i] == group[i]
		}
		if match {
			return r
		}
	}
	t.Fatalf("no row for group %v in %+v", group, rows)
	return AnalyzeRow{}
}

func TestAnalyzeCountAndPercentilesByPath(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount}, AnalyzeCalc{Fn: CalcP50}, AnalyzeCalc{Fn: CalcP95},
		AnalyzeCalc{Fn: CalcP99}, AnalyzeCalc{Fn: CalcErrorRate}, AnalyzeCalc{Fn: CalcSumDuration},
		AnalyzeCalc{Fn: CalcAvgDuration}, AnalyzeCalc{Fn: CalcMaxDuration},
		AnalyzeCalc{Fn: CalcCountDistinct, Key: "user"})
	q.GroupBy = []string{"url.path"}
	expr, err := filter.Parse(`{"filters":[{"key":"kind","op":"=","value":"server"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	q.Expr = expr

	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Rows) != 2 || res.Other != nil {
		t.Fatalf("rows = %+v other = %+v", res.Rows, res.Other)
	}
	if res.Rows[0].Group[0] != "/a" {
		t.Errorf("rows must sort by count descending, got %v first", res.Rows[0].Group)
	}
	a := rowFor(t, res.Rows, "/a")
	want := []float64{10, 5000, 10000, 10000, 0.2, 55000, 5500, 10000, 3}
	for i, w := range want {
		if a.Values[i] != w {
			t.Errorf("/a calc %d = %v, want %v", i, a.Values[i], w)
		}
	}
	b := rowFor(t, res.Rows, "/b")
	if b.Count != 4 || b.Values[1] != 100 || b.Values[4] != 0 {
		t.Errorf("/b = %+v", b)
	}
	if res.SampleEvery != 1 || res.Scanned != 14 || res.Truncated {
		t.Errorf("scan = %+v", res)
	}
}

func TestAnalyzeGroupByTwoKeysMatchesManualCount(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.GroupBy = []string{"url.path", "http.response.status_code"}
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, res.Rows, "/a", "200").Count; got != 10 {
		t.Errorf("/a 200 = %d", got)
	}
	if got := rowFor(t, res.Rows, "/b", "500").Count; got != 4 {
		t.Errorf("/b 500 = %d", got)
	}
	if got := rowFor(t, res.Rows, "/c", "").Count; got != 1 {
		t.Errorf("/c without status = %d", got)
	}
	var manual int64
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM spans WHERE project_id = 1
		AND json_extract(attributes, '$."url.path"') = '/a'
		AND json_extract(attributes, '$."http.response.status_code"') = 200`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual != 10 {
		t.Fatalf("manual count = %d", manual)
	}
}

func TestAnalyzeGroupCapFoldsTheRestIntoOther(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount}, AnalyzeCalc{Fn: CalcP95}, AnalyzeCalc{Fn: CalcCountDistinct, Key: "user"})
	q.GroupBy = []string{"url.path"}
	q.Limit = 1
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Group[0] != "/a" {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.Other == nil || !res.Other.Other || res.Other.Count != 6 {
		t.Fatalf("other = %+v", res.Other)
	}
	if res.Other.Values[1] != 100 {
		t.Errorf("other p95 = %v", res.Other.Values[1])
	}
	if res.Scanned != 16 {
		t.Errorf("scanned = %d", res.Scanned)
	}
}

func TestAnalyzeNoGroupByReturnsOneTotalRow(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount}, AnalyzeCalc{Fn: CalcP50})
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Count != 16 || len(res.Rows[0].Group) != 0 {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestAnalyzeEmptyWindowReturnsNoRows(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.From = time.Now().UTC().Add(48 * time.Hour)
	q.To = q.From.Add(time.Hour)
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestAnalyzeSeriesBucketsAndFoldsOther(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount}, AnalyzeCalc{Fn: CalcP95})
	q.GroupBy = []string{"url.path"}
	q.BucketSeconds = 3600
	q.Only = [][]string{{"/a"}}
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	var a, other int64
	for _, r := range res.Rows {
		if r.Bucket <= 0 || r.Bucket%3600 != 0 || time.Since(time.Unix(r.Bucket, 0)) > 2*time.Hour {
			t.Errorf("bucket %d is not aligned", r.Bucket)
		}
		switch {
		case r.Other:
			other += r.Count
		case r.Group[0] == "/a":
			a += r.Count
		}
	}
	if a != 10 || other != 6 {
		t.Errorf("a = %d other = %d rows = %+v", a, other, res.Rows)
	}
}

func TestAnalyzeAutoSampleKicksInAboveTheRowCap(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.GroupBy = []string{"url.path"}
	q.MaxSpans = 8
	res, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if res.SampleEvery < 2 {
		t.Fatalf("SampleEvery = %d, want a sampled scan", res.SampleEvery)
	}
	if res.Scanned > 8 {
		t.Errorf("scanned %d rows over a cap of 8", res.Scanned)
	}

	q.Sample = 1
	exact, err := repo.Analyze(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	if exact.SampleEvery != 1 || !exact.Truncated || exact.Scanned != 8 {
		t.Errorf("explicit exact scan = %+v", exact)
	}
}

func TestAnalyzeRejectsAnInvalidFilter(t *testing.T) {
	repo := seedAnalyze(t)
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.Expr = &filter.Expr{Filters: []filter.Node{{Key: "duration_us", Op: filter.OpGt, Value: "abc"}}}
	if _, err := repo.Analyze(t.Context(), q); err == nil {
		t.Fatal("want an error for a non numeric duration")
	}
}
