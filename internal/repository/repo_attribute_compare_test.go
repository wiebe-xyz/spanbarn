package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func compareWindow(expr *filter.Expr) AttributeSetWindow {
	now := time.Now().UTC()
	return AttributeSetWindow{
		ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour),
		Expr: expr, Sample: 1, MaxSpans: 1000, ValueCap: 200,
	}
}

func seedCompareSpans(t *testing.T, repo *Repository) {
	t.Helper()
	var spans []Span
	for i := 0; i < 4; i++ {
		spans = append(spans, attrSpan(i, "GET /a", "web",
			fmt.Sprintf(`{"user_agent.original":"Mozilla","kind":"attr-kind","n":%d}`, i)))
	}
	for i := 4; i < 10; i++ {
		spans = append(spans, attrSpan(i, "GET /a", "api", `{"user_agent.original":"curl","region":"eu"}`))
	}
	spans = append(spans, attrSpan(10, "GET /a", "api", `not json`))
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
}

func TestScanAttributeSetDistributions(t *testing.T) {
	repo := setupTestDB(t)
	seedCompareSpans(t, repo)

	scan, err := repo.ScanAttributeSet(context.Background(), compareWindow(nil))
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 10 {
		t.Fatalf("scanned = %d, want 10 (invalid json skipped)", scan.Scanned)
	}
	ua := scan.Keys["user_agent.original"]
	if ua.Counts["Mozilla"] != 4 || ua.Counts["curl"] != 6 {
		t.Errorf("user agent counts = %+v", ua.Counts)
	}
	if scan.Keys["service"].Counts["api"] != 6 || scan.Keys["service"].Counts["web"] != 4 {
		t.Errorf("service column counts = %+v", scan.Keys["service"].Counts)
	}
	// An attribute named like a span column is keyed with the attributes prefix.
	if scan.Keys["attributes.kind"].Counts["attr-kind"] != 4 || scan.Keys["kind"].Counts["server"] != 10 {
		t.Errorf("kind: %+v %+v", scan.Keys["attributes.kind"], scan.Keys["kind"])
	}
}

func TestScanAttributeSetAppliesExpr(t *testing.T) {
	repo := setupTestDB(t)
	seedCompareSpans(t, repo)

	expr := &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "service", Op: filter.OpEq, Value: "web"}}}
	scan, err := repo.ScanAttributeSet(context.Background(), compareWindow(expr))
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 4 || scan.Keys["user_agent.original"].Counts["Mozilla"] != 4 {
		t.Fatalf("selection scan = %+v", scan)
	}
	if _, ok := scan.Keys["region"]; ok {
		t.Error("region belongs to the api spans only")
	}
}

func TestScanAttributeSetSumsValuesBeyondTheCap(t *testing.T) {
	repo := setupTestDB(t)
	var spans []Span
	for i := 0; i < 30; i++ {
		spans = append(spans, attrSpan(i, "GET /a", "web", fmt.Sprintf(`{"request.id":"r%02d"}`, i)))
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	w := compareWindow(nil)
	w.ValueCap = 5
	scan, err := repo.ScanAttributeSet(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	v := scan.Keys["request.id"]
	if len(v.Counts) != 5 || v.Other != 25 {
		t.Fatalf("kept %d values, other %d", len(v.Counts), v.Other)
	}
}

func TestScanAttributeSetBoundsTheScan(t *testing.T) {
	repo := setupTestDB(t)
	seedCompareSpans(t, repo)

	w := compareWindow(nil)
	w.MaxSpans = 3
	scan, err := repo.ScanAttributeSet(context.Background(), w)
	if err != nil || scan.Scanned != 3 {
		t.Fatalf("scanned = %d err = %v, want the row cap of 3", scan.Scanned, err)
	}
	w = compareWindow(nil)
	w.Sample = 2
	scan, err = repo.ScanAttributeSet(context.Background(), w)
	if err != nil || scan.Scanned >= 10 || scan.Scanned == 0 {
		t.Fatalf("sampled scanned = %d err = %v", scan.Scanned, err)
	}
}

func TestScanAttributeSetRejectsBadInput(t *testing.T) {
	repo := setupTestDB(t)
	w := compareWindow(nil)
	w.From = time.Time{}
	if _, err := repo.ScanAttributeSet(context.Background(), w); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("missing from: %v", err)
	}
	bad := &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "x", Op: "nope"}}}
	if _, err := repo.ScanAttributeSet(context.Background(), compareWindow(bad)); err == nil {
		t.Error("an invalid operator should fail")
	}
}

func TestScanAttributeSetEmpty(t *testing.T) {
	repo := setupTestDB(t)
	scan, err := repo.ScanAttributeSet(context.Background(), compareWindow(nil))
	if err != nil || scan.Scanned != 0 || len(scan.Keys) != 0 {
		t.Fatalf("scan = %+v err = %v", scan, err)
	}
}
