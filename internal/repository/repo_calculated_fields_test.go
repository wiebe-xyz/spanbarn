package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// seedCalc seeds the analyze fixture and the two projects it refers to.
func seedCalc(t *testing.T) *Repository {
	t.Helper()
	repo := seedAnalyze(t)
	for _, slug := range []string{"one", "two"} {
		if _, err := repo.CreateProject(slug, slug); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}
	return repo
}

func addField(t *testing.T, repo *Repository, project int64, name, expr string) int64 {
	t.Helper()
	id, err := repo.CreateCalculatedField(CalculatedField{ProjectID: project, Name: name, Expression: expr})
	if err != nil {
		t.Fatalf("CreateCalculatedField(%s): %v", name, err)
	}
	return id
}

func TestCalculatedFieldCRUD(t *testing.T) {
	repo := seedCalc(t)
	id := addField(t, repo, 1, "ms", "duration_us / 1000")
	addField(t, repo, 2, "ms", "duration_us / 2000")

	if _, err := repo.CreateCalculatedField(CalculatedField{ProjectID: 1, Name: "ms", Expression: "1"}); !errors.Is(err, ErrDuplicateField) {
		t.Fatalf("duplicate: got %v", err)
	}
	got, err := repo.GetCalculatedField(id)
	if err != nil || got.Name != "ms" || got.Expression != "duration_us / 1000" || got.ProjectID != 1 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := repo.UpdateCalculatedField(id, "millis", "duration_us / 1000.0"); err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListCalculatedFields(1)
	if err != nil || len(list) != 1 || list[0].Name != "millis" {
		t.Fatalf("list project 1: %+v %v", list, err)
	}
	if err := repo.UpdateCalculatedField(id, "ms", "1"); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	if err := repo.UpdateCalculatedField(9999, "x", "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := repo.DeleteCalculatedField(id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetCalculatedField(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := repo.DeleteCalculatedField(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if other, _ := repo.ListCalculatedFields(2); len(other) != 1 {
		t.Fatalf("project 2 lost its field: %+v", other)
	}
}

func calcSpanIDs(t *testing.T, repo *Repository, project int64, raw string) []string {
	t.Helper()
	expr, err := filter.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spans, err := repo.QuerySpans(SpanFilter{
		ProjectID: project, Expr: expr, From: time.Now().UTC().Add(-time.Hour), Limit: 100,
	})
	if err != nil {
		t.Fatalf("QuerySpans(%s): %v", raw, err)
	}
	ids := make([]string, 0, len(spans))
	for _, s := range spans {
		ids = append(ids, s.SpanID)
	}
	sort.Strings(ids)
	return ids
}

func TestCalculatedFieldInFilter(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "ms", "duration_us / 1000")

	pairs := []struct{ calc, plain string }{
		{`{"filters":[{"key":"ms","op":">=","value":5}]}`, `{"filters":[{"key":"duration_us","op":">=","value":5000}]}`},
		{`{"filters":[{"key":"ms","op":"<","value":0.1}]}`, `{"filters":[{"key":"duration_us","op":"<","value":100}]}`},
		{`{"filters":[{"key":"ms","op":"=","value":3}]}`, `{"filters":[{"key":"duration_us","op":"=","value":3000}]}`},
		{`{"filters":[{"key":"ms","op":"!=","value":3}]}`, `{"filters":[{"key":"duration_us","op":"!=","value":3000}]}`},
		{`{"filters":[{"key":"ms","op":"in","values":[1,2]}]}`, `{"filters":[{"key":"duration_us","op":"in","values":[1000,2000]}]}`},
		{`{"filters":[{"key":"ms","op":"exists"}]}`, `{"filters":[{"key":"duration_us","op":"exists"}]}`},
		{`{"filters":[{"key":"ms","op":"does-not-exist"}]}`, `{"filters":[{"key":"duration_us","op":"does-not-exist"}]}`},
		{`{"filters":[{"key":"ms","op":"starts-with","value":"1"}]}`, `{"filters":[{"key":"duration_us","op":"in","values":[1000,10000]}]}`},
	}
	for _, p := range pairs {
		got, want := calcSpanIDs(t, repo, 1, p.calc), calcSpanIDs(t, repo, 1, p.plain)
		if len(want) == 0 && p.plain != pairs[6].plain {
			t.Fatalf("fixture gives no rows for %s", p.plain)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s\n got %v\nwant %v", p.calc, got, want)
		}
	}
}

func TestCalculatedFieldUsesAttributes(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "route", "coalesce(url.path, name)")
	addField(t, repo, 1, "label", "concat(lower(user), ':', route)")

	got := calcSpanIDs(t, repo, 1, `{"filters":[{"key":"route","op":"=","value":"/c"}]}`)
	if !reflect.DeepEqual(got, []string{"s15"}) {
		t.Fatalf("route /c: %v", got)
	}
	// s16 has no url.path, so route falls back to the span name.
	got = calcSpanIDs(t, repo, 1, `{"filters":[{"key":"route","op":"=","value":"op"}]}`)
	if !reflect.DeepEqual(got, []string{"s16"}) {
		t.Fatalf("route fallback: %v", got)
	}
	// A field built on another field.
	got = calcSpanIDs(t, repo, 1, `{"filters":[{"key":"label","op":"=","value":"anon:op"}]}`)
	if !reflect.DeepEqual(got, []string{"s16"}) {
		t.Fatalf("nested field: %v", got)
	}
}

func TestCalculatedFieldScopedToProject(t *testing.T) {
	repo := seedCalc(t)
	// Only project 2 defines "ms". In project 1 the key reads as an attribute
	// of that name, which no span has.
	addField(t, repo, 2, "ms", "duration_us / 1000")
	if got := calcSpanIDs(t, repo, 1, `{"filters":[{"key":"ms","op":">=","value":0}]}`); len(got) != 0 {
		t.Fatalf("project 1 saw project 2's field: %v", got)
	}
	// Project 2's field answers for project 2's spans only.
	if got := calcSpanIDs(t, repo, 2, `{"filters":[{"key":"ms","op":">=","value":0}]}`); !reflect.DeepEqual(got, []string{"s17"}) {
		t.Fatalf("project 2: %v", got)
	}
	// The same name in both projects keeps each project's own meaning.
	addField(t, repo, 1, "ms", "duration_us / 1")
	if got := calcSpanIDs(t, repo, 1, `{"filters":[{"key":"ms","op":"=","value":9000}]}`); !reflect.DeepEqual(got, []string{"s9"}) {
		t.Fatalf("project 1 own definition: %v", got)
	}
}

func TestCalculatedFieldBuiltinColumnWins(t *testing.T) {
	repo := seedCalc(t)
	// Stored directly, as a row from before a validation rule would be.
	addField(t, repo, 1, "duration_us", "1")
	got := calcSpanIDs(t, repo, 1, `{"filters":[{"key":"duration_us","op":"=","value":50}]}`)
	if !reflect.DeepEqual(got, []string{"s15"}) {
		t.Fatalf("the column must win over a field of the same name: %v", got)
	}
}

func TestCalculatedFieldInGroupBy(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "bucket", "if(duration_us >= 5000, 'slow', 'fast')")
	addField(t, repo, 1, "ms", "duration_us / 1000")

	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.GroupBy = []string{"bucket"}
	res, err := repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, res.Rows, "slow").Count; got != 6 {
		t.Fatalf("slow = %d, want 6", got)
	}
	if got := rowFor(t, res.Rows, "fast").Count; got != 10 {
		t.Fatalf("fast = %d, want 10", got)
	}

	// Group by a numeric field and compare with the hand-written equivalent.
	q.GroupBy = []string{"ms"}
	byCalc, err := repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	q.GroupBy = []string{"duration_us"}
	byColumn, err := repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, byCalc.Rows, "3").Count; got != rowFor(t, byColumn.Rows, "3000").Count {
		t.Fatalf("ms=3 count %d differs from duration_us=3000", got)
	}
	// A whole number reads as "3", a fraction as "0.1".
	if got := rowFor(t, byCalc.Rows, "0.1").Count; got != 4 {
		t.Fatalf("ms=0.1 count %d, want 4", got)
	}

	// A calculated field filters and groups in one query.
	q.GroupBy = []string{"bucket"}
	q.Expr = mustParseFilter(t, `{"filters":[{"key":"ms","op":">=","value":9}]}`)
	res, err = repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || rowFor(t, res.Rows, "slow").Count != 2 {
		t.Fatalf("filtered group-by: %+v", res.Rows)
	}

	// count_distinct reads a calculated field too.
	q.Expr = nil
	q.GroupBy = []string{"kind"}
	q.Calcs = []AnalyzeCalc{{Fn: CalcCountDistinct, Key: "bucket"}}
	res, err = repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, res.Rows, "server").Values[0]; got != 2 {
		t.Fatalf("distinct buckets on server spans = %v, want 2", got)
	}
}

func TestCalculatedFieldGroupByScopedToProject(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 2, "bucket", "'two'")
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.GroupBy = []string{"bucket"}
	res, err := repo.Analyze(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	// Project 1 has no "bucket" field: every span groups under "" (no attribute).
	if len(res.Rows) != 1 || res.Rows[0].Group[0] != "" || res.Rows[0].Count != 16 {
		t.Fatalf("rows: %+v", res.Rows)
	}
}

func mustParseFilter(t *testing.T, raw string) *filter.Expr {
	t.Helper()
	e, err := filter.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCalculatedFieldInAttributeScanAndTraceSearch(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "ms", "duration_us / 1000")
	expr := mustParseFilter(t, `{"filters":[{"key":"ms","op":">=","value":9}]}`)

	w := compareWindow(expr)
	w.ProjectID = 1
	scan, err := repo.ScanAttributeSet(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 2 {
		t.Fatalf("scanned %d spans, want 2", scan.Scanned)
	}

	rows, err := repo.SearchTraceSummaries(SpanFilter{
		ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var traces []string
	for _, r := range rows {
		traces = append(traces, r.TraceID)
	}
	sort.Strings(traces)
	if fmt.Sprint(traces) != "[t10 t9]" {
		t.Fatalf("traces %v", traces)
	}
}

func TestCalculatedFieldCycleIsAnErrorNotNull(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "a", "b + 1")
	addField(t, repo, 1, "b", "a + 1")
	expr := mustParseFilter(t, `{"filters":[{"key":"a","op":">","value":0}]}`)
	_, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour)})
	if !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("filter on a cycle: %v", err)
	}
	q := baseQuery(AnalyzeCalc{Fn: CalcCount})
	q.GroupBy = []string{"b"}
	if _, err := repo.Analyze(context.Background(), q); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("group by a cycle: %v", err)
	}
	// A query that does not touch the cycle still works.
	if got := calcSpanIDs(t, repo, 1, `{"filters":[{"key":"duration_us","op":"=","value":50}]}`); len(got) != 1 {
		t.Fatalf("unrelated query: %v", got)
	}
}

// TestCalculatedFieldInjection stores hostile text as rows, as a client that
// skipped validation could, and checks that nothing runs and the query fails
// closed.
func TestCalculatedFieldInjection(t *testing.T) {
	repo := seedCalc(t)
	hostile := []string{
		"1; DROP TABLE spans",
		"'x'; DELETE FROM spans; --",
		"duration_us /* c */ + 1",
		`"a"`,
		"coalesce(1, 2) UNION SELECT 1",
	}
	for i, h := range hostile {
		addField(t, repo, 1, fmt.Sprintf("bad%d", i), h)
	}
	for i := range hostile {
		key := fmt.Sprintf("bad%d", i)
		expr := mustParseFilter(t, fmt.Sprintf(`{"filters":[{"key":%q,"op":"exists"}]}`, key))
		if _, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour)}); !errors.Is(err, filter.ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", key, err)
		}
	}
	// Hostile text in a filter value and in a literal of a valid field stays data.
	for i := range hostile {
		if err := repo.DeleteCalculatedField(int64(i + 1)); err != nil {
			t.Fatal(err)
		}
	}
	addField(t, repo, 1, "tag", "concat('x''); DROP TABLE spans; --', name)")
	expr := mustParseFilter(t, `{"filters":[{"key":"tag","op":"=","value":"x'); DROP TABLE spans; --op"}]}`)
	got, err := repo.QuerySpans(SpanFilter{ProjectID: 1, Expr: expr, From: time.Now().UTC().Add(-time.Hour), Limit: 100})
	if err != nil || len(got) != 16 {
		t.Fatalf("literal as data: %d spans, %v", len(got), err)
	}
	var n int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM spans`).Scan(&n); err != nil || n != 17 {
		t.Fatalf("spans table damaged: n=%d err=%v", n, err)
	}
}

func TestPreviewCalculatedField(t *testing.T) {
	repo := seedCalc(t)
	addField(t, repo, 1, "ms", "duration_us / 1000")
	got, err := repo.PreviewCalculatedField(context.Background(), 1, "slow", "if(ms > 1, 'y', 'n')")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != previewSpans || got[0].SpanID == "" {
		t.Fatalf("preview: %+v", got)
	}
	for _, s := range got {
		if s.Value != "y" && s.Value != "n" {
			t.Fatalf("value %v", s.Value)
		}
	}
	// An edit of an existing field previews the new expression.
	got, err = repo.PreviewCalculatedField(context.Background(), 1, "ms", "1 + 1")
	if err != nil || got[0].Value != int64(2) {
		t.Fatalf("replace: %+v %v", got, err)
	}
	if _, err := repo.PreviewCalculatedField(context.Background(), 1, "loop", "loop + 1"); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := repo.PreviewCalculatedField(context.Background(), 1, "x", "1;"); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("bad expression: %v", err)
	}
	if _, err := repo.PreviewCalculatedField(context.Background(), 1, "duration_us", "1"); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("column name: %v", err)
	}
}
