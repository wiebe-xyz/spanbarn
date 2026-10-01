package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func analyzeReq() AnalyzeRequest {
	now := time.Now().UTC()
	return AnalyzeRequest{ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
}

func TestAnalyzeValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	mutate := map[string]func(*AnalyzeRequest){
		"project":      func(r *AnalyzeRequest) { r.ProjectID = 0 },
		"range":        func(r *AnalyzeRequest) { r.From = time.Time{} },
		"order":        func(r *AnalyzeRequest) { r.To = r.From },
		"wide":         func(r *AnalyzeRequest) { r.From = r.To.Add(-31 * 24 * time.Hour) },
		"sample":       func(r *AnalyzeRequest) { r.Sample = 5000 },
		"calc":         func(r *AnalyzeRequest) { r.Calcs = []string{"mean"} },
		"distinct":     func(r *AnalyzeRequest) { r.Calcs = []string{"count_distinct:"} },
		"key on count": func(r *AnalyzeRequest) { r.Calcs = []string{"count:x"} },
		"dup group":    func(r *AnalyzeRequest) { r.GroupBy = []string{"a", "a"} },
		"bad key":      func(r *AnalyzeRequest) { r.GroupBy = []string{`a"b`} },
		"order_by":     func(r *AnalyzeRequest) { r.Calcs = []string{"count"}; r.OrderBy = "p99" },
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			req := analyzeReq()
			m(&req)
			if _, err := svc.Analyze(context.Background(), req); !errors.Is(err, filter.ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func seedAnalyzeService(t *testing.T) *QueryService {
	t.Helper()
	repo := setupTestRepo(t)
	var spans []repository.Span
	for i := 0; i < 30; i++ {
		path := "/busy"
		if i >= 20 {
			path = fmt.Sprintf("/rare%d", i)
		}
		spans = append(spans, repository.Span{
			ProjectID: 1, TraceID: fmt.Sprintf("t%d", i), SpanID: fmt.Sprintf("s%d", i), Name: "op",
			Service: "web", Kind: "server", Status: "ok", StartTimeUs: 1000, DurationUs: int64(i + 1),
			Attributes: fmt.Sprintf(`{"url.path":%q}`, path), Events: "[]",
		})
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	return NewQueryService(repo, nil, nil)
}

func TestAnalyzeTableCapsGroupsAndBuildsDrillFilters(t *testing.T) {
	svc := seedAnalyzeService(t)
	req := analyzeReq()
	req.GroupBy = []string{"url.path"}
	req.Calcs = []string{"count", "max_duration"}
	req.Limit = 3
	base, err := filter.Parse(`{"filters":[{"key":"kind","op":"=","value":"server"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	req.Expr = base

	res, err := svc.Analyze(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 || res.Rows[0].Group[0] != "/busy" || res.Rows[0].Count != 20 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	if res.Other == nil || res.Other.Count != 8 || res.Other.Drill != nil {
		t.Fatalf("other = %+v", res.Other)
	}
	drill := res.Rows[0].Drill
	if drill == nil || len(drill.Filters) != 2 || drill.Filters[1].Key != "url.path" {
		t.Fatalf("drill = %+v", drill)
	}
	if res.Rows[0].Values[1] != 20 || res.SampleEvery != 1 || res.Truncated {
		t.Errorf("values = %v sample = %d truncated = %v", res.Rows[0].Values, res.SampleEvery, res.Truncated)
	}
}

func TestAnalyzeScalesCountsWhenSampled(t *testing.T) {
	svc := seedAnalyzeService(t)
	req := analyzeReq()
	req.Calcs = []string{"count", "sum_duration", "max_duration"}
	req.MaxSpans = 10

	res, err := svc.Analyze(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.SampleEvery < 2 || len(res.Rows) != 1 {
		t.Fatalf("res = %+v", res)
	}
	row := res.Rows[0]
	if row.Count < 20 || row.Count > 40 {
		t.Errorf("scaled count = %d, want near 30", row.Count)
	}
	if row.Values[0] != float64(row.Count) || row.Values[2] > 30 {
		t.Errorf("values = %v", row.Values)
	}
}

func TestAnalyzeSeriesAlignsLinesToBuckets(t *testing.T) {
	svc := seedAnalyzeService(t)
	req := analyzeReq()
	req.GroupBy = []string{"url.path"}
	req.Calcs = []string{"count"}
	req.Limit = 1

	res, err := svc.AnalyzeSeries(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.BucketSeconds != 300 || len(res.Buckets) < 24 {
		t.Fatalf("bucket = %d buckets = %d", res.BucketSeconds, len(res.Buckets))
	}
	if len(res.Series) != 2 || res.Series[0].Group[0] != "/busy" || !res.Series[1].Other {
		t.Fatalf("series = %+v", res.Series)
	}
	var busy, other float64
	for i := range res.Buckets {
		busy += *res.Series[0].Values[i]
		other += *res.Series[1].Values[i]
	}
	if busy != 20 || other != 10 {
		t.Errorf("busy = %v other = %v", busy, other)
	}

	req.Calcs = []string{"count", "p95"}
	if _, err := svc.AnalyzeSeries(context.Background(), req); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("two calculations: %v", err)
	}
	req.Calcs = []string{"count"}
	req.BucketSeconds = 1
	if _, err := svc.AnalyzeSeries(context.Background(), req); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("tiny bucket: %v", err)
	}
}

func TestPickBucket(t *testing.T) {
	cases := map[int64]int64{3600: 300, 24 * 3600: 1800, 7 * 24 * 3600: 6 * 3600, 30 * 24 * 3600: 86400}
	for rng, want := range cases {
		if got := pickBucket(rng); got != want {
			t.Errorf("pickBucket(%d) = %d, want %d", rng, got, want)
		}
	}
}
