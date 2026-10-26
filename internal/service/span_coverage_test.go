package service

import (
	"context"
	"testing"
	"time"
)

func TestDependenciesAndDatabaseReportTruncatedCoverage(t *testing.T) {
	repo := setupTestRepo(t)
	if err := repo.InsertSpans(dbTestSpans(time.Now())); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	limit := spanReadLimit
	t.Cleanup(func() { spanReadLimit = limit })
	ctx := context.Background()

	for _, tc := range []struct {
		limit     int
		truncated bool
	}{{2, true}, {10, false}} {
		spanReadLimit = tc.limit
		// A fresh service per limit, so the read cache does not answer for the other.
		svc := NewQueryService(repo, nil, nil)
		_, depCov, err := svc.ListDependencies(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil {
			t.Fatalf("ListDependencies: %v", err)
		}
		_, dbCov, err := svc.ListDatabaseQueries(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil {
			t.Fatalf("ListDatabaseQueries: %v", err)
		}
		for name, c := range map[string]Coverage{"dependencies": depCov, "database": dbCov} {
			if c.Truncated != tc.truncated || (c.CoveredFrom != nil) != tc.truncated {
				t.Errorf("limit %d, %s: Truncated = %v, CoveredFrom = %v; want truncated %v with a start only then",
					tc.limit, name, c.Truncated, c.CoveredFrom, tc.truncated)
			}
		}
	}
}
