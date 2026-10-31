package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func TestSpanLedgerEndpoint(t *testing.T) {
	srv, sm, repo := setupQueryTestServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-time.Hour).UTC()
	bucket := old.Unix() / model.SpanLedgerBucketSeconds * model.SpanLedgerBucketSeconds
	if err := repo.AddSpanLedgerRows([]model.SpanLedgerRow{
		{Bucket: bucket, ProjectID: 1, Accepted: 10, Forwarded: 8, Processed: 8},
		{Bucket: bucket, ProjectID: 2, Accepted: 5, Processed: 5},
	}); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/span-ledger?project_id=1&from=" + old.Add(-time.Hour).Format(time.RFC3339) +
		"&to=" + time.Now().UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var entries []service.SpanLedgerEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ProjectID != 1 {
		t.Fatalf("entries = %+v, want project 1 only", entries)
	}
	if !entries[0].Settled || entries[0].LostSpans != 2 {
		t.Errorf("entry = %+v, want settled with 2 spans lost", entries[0])
	}

	req = httptest.NewRequest(http.MethodGet, path, nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status %d, want 401", rec.Code)
	}
}
