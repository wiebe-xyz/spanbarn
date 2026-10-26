package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func TestWriteCoverageSetsTheHeaderOnlyWhenTruncated(t *testing.T) {
	from := time.Date(2026, 10, 26, 13, 1, 50, 0, time.FixedZone("CET", 3600))

	rec := httptest.NewRecorder()
	writeCoverage(rec, service.Coverage{Truncated: true, CoveredFrom: &from})
	if got := rec.Header().Get(coveredFromHeader); got != "2026-10-26T12:01:50Z" {
		t.Errorf("truncated: header %q, want the UTC start", got)
	}

	rec = httptest.NewRecorder()
	writeCoverage(rec, service.Coverage{})
	if got := rec.Header().Get(coveredFromHeader); got != "" {
		t.Errorf("full read: header %q, want none", got)
	}
}
