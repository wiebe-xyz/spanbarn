package repository

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func attrSpan(i int, name, service, attrs string) Span {
	return Span{
		ProjectID: 1, TraceID: fmt.Sprintf("t%d", i), SpanID: fmt.Sprintf("s%d", i),
		Name: name, Service: service, Kind: "server", Status: "ok",
		StartTimeUs: 1, DurationUs: 1, Attributes: attrs, Events: "[]",
	}
}

func attrWindow() AttributeWindow {
	now := time.Now().UTC()
	return AttributeWindow{
		ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour),
		Sample: 1, MaxSpans: 1000, MaxKeys: 50, TopValues: 5, DistinctCap: 1000,
	}
}

func findKey(t *testing.T, scan *AttributeScan, key string) AttributeKeyStats {
	t.Helper()
	for _, k := range scan.Keys {
		if k.Key == key {
			return k
		}
	}
	t.Fatalf("key %q not in %+v", key, scan.Keys)
	return AttributeKeyStats{}
}

func seedAttrSpans(t *testing.T, repo *Repository) {
	t.Helper()
	var spans []Span
	for i := 0; i < 6; i++ {
		spans = append(spans, attrSpan(i, "POST /presign", "web",
			fmt.Sprintf(`{"client.address":"10.0.0.%d","http.response.status_code":200,"user_agent.original":"curl"}`, i%3)))
	}
	spans = append(spans,
		attrSpan(6, "POST /presign", "web", `{"http.response.status_code":500,"nested":{"a":1},"gone":null}`),
		attrSpan(7, "GET /health", "web", `{"http.response.status_code":200}`),
		attrSpan(8, "SELECT", "db", `not json`),
	)
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
}

func TestScanAttributesKeysCoverageAndTopValues(t *testing.T) {
	repo := setupTestDB(t)
	seedAttrSpans(t, repo)

	w := attrWindow()
	w.SpanName = "POST /presign"
	scan, err := repo.ScanAttributes(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 7 {
		t.Fatalf("scanned = %d, want 7", scan.Scanned)
	}

	addr := findKey(t, scan, "client.address")
	if addr.Spans != 6 || addr.Distinct != 3 || addr.DistinctCapped {
		t.Errorf("client.address = %+v", addr)
	}
	if len(addr.Top) != 3 || addr.Top[0].Count != 2 {
		t.Errorf("client.address top = %+v", addr.Top)
	}
	status := findKey(t, scan, "http.response.status_code")
	if status.Spans != 7 || status.Top[0].Value != "200" || status.Top[0].Count != 6 || status.Top[1].Value != "500" {
		t.Errorf("status = %+v", status)
	}
	// Keys are ordered by how many spans populate them.
	if scan.Keys[0].Key != "http.response.status_code" {
		t.Errorf("first key = %q", scan.Keys[0].Key)
	}
	for _, k := range scan.Keys {
		if k.Key == "gone" || k.Key == "nested" {
			t.Errorf("null and nested values are not listed, got %q", k.Key)
		}
	}
}

func TestScanAttributesFiltersAndMalformedJSON(t *testing.T) {
	repo := setupTestDB(t)
	seedAttrSpans(t, repo)

	// The malformed span is skipped, not an error: 8 valid spans in the project.
	scan, err := repo.ScanAttributes(context.Background(), attrWindow())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 8 {
		t.Errorf("scanned = %d, want 8", scan.Scanned)
	}

	w := attrWindow()
	w.Service = "db"
	scan, err = repo.ScanAttributes(context.Background(), w)
	if err != nil || scan.Scanned != 0 || len(scan.Keys) != 0 {
		t.Errorf("db service: %+v, %v", scan, err)
	}

	w = attrWindow()
	w.ProjectID = 2
	scan, err = repo.ScanAttributes(context.Background(), w)
	if err != nil || scan.Scanned != 0 {
		t.Errorf("other project must see nothing: %+v, %v", scan, err)
	}

	w = attrWindow()
	w.From, w.To = time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
	scan, err = repo.ScanAttributes(context.Background(), w)
	if err != nil || scan.Scanned != 0 {
		t.Errorf("window before the spans: %+v, %v", scan, err)
	}
}

func TestScanAttributesCapsAndSingleKey(t *testing.T) {
	repo := setupTestDB(t)
	seedAttrSpans(t, repo)

	w := attrWindow()
	w.MaxSpans = 3
	scan, err := repo.ScanAttributes(context.Background(), w)
	if err != nil || scan.Scanned != 3 {
		t.Fatalf("row cap: %+v, %v", scan, err)
	}

	w = attrWindow()
	w.MaxKeys = 2
	scan, _ = repo.ScanAttributes(context.Background(), w)
	if len(scan.Keys) != 2 {
		t.Errorf("key cap: got %d keys", len(scan.Keys))
	}

	w = attrWindow()
	w.TopValues = 1
	scan, _ = repo.ScanAttributes(context.Background(), w)
	for _, k := range scan.Keys {
		if len(k.Top) != 1 {
			t.Errorf("top cap: %s has %d values", k.Key, len(k.Top))
		}
	}

	w = attrWindow()
	w.DistinctCap = 2
	scan, _ = repo.ScanAttributes(context.Background(), w)
	if addr := findKey(t, scan, "client.address"); !addr.DistinctCapped || addr.Distinct != 2 {
		t.Errorf("distinct cap: %+v", addr)
	}

	w = attrWindow()
	w.Key = "client.address"
	scan, _ = repo.ScanAttributes(context.Background(), w)
	if len(scan.Keys) != 1 || scan.Keys[0].Key != "client.address" {
		t.Errorf("single key: %+v", scan.Keys)
	}
}

func TestScanAttributesSamplesByID(t *testing.T) {
	repo := setupTestDB(t)
	var spans []Span
	for i := 0; i < 40; i++ {
		spans = append(spans, attrSpan(i, "op", "web", `{"k":"v"}`))
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	w := attrWindow()
	w.Sample = 4
	scan, err := repo.ScanAttributes(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Scanned != 10 {
		t.Errorf("1-in-4 of 40 spans scanned = %d, want 10", scan.Scanned)
	}
}
